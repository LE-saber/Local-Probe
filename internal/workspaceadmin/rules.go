package workspaceadmin

import (
	"context"
	"errors"
	"slices"
	"sort"
	"unicode"

	"github.com/LE-saber/Local-Probe/internal/config"
	"github.com/LE-saber/Local-Probe/internal/policy"
	"github.com/LE-saber/Local-Probe/internal/readcore"
)

const (
	MaxRulePatterns         = 256
	MaxRuleBytes            = 64 << 10
	MaxRuleSamples          = 128
	CodeInvalidRules   Code = "invalid_rules"
	CodeImpactRequired Code = "impact_acknowledgement_required"
)

var (
	ErrInvalidRules   = errors.New("invalid file rules")
	ErrImpactRequired = errors.New("exact affected connections acknowledgement required")
)

// RulePatterns are existing policy globs, not regex or executable rules.
// Ignore is advisory discovery filtering; it never grants access through deny.
type RulePatterns struct {
	Deny   []string `json:"deny_patterns"`
	Ignore []string `json:"ignore_patterns"`
}

type RootRules struct {
	RootID  string `json:"root_id"`
	Enabled bool   `json:"enabled"`
	RulePatterns
}

// Rules contains only the selected connection's profile and registered roots.
// No physical paths, credentials, other profiles, or command bodies are returned.
type Rules struct {
	ConnectionID string       `json:"connection_id"`
	ProfileID    string       `json:"profile_id"`
	Revision     string       `json:"revision"`
	Profile      RulePatterns `json:"profile"`
	Roots        []RootRules  `json:"roots"`
}

// RuleUpdate replaces a complete deny/ignore list, independently. Nil means
// unchanged; an explicit empty slice clears that list. An empty RootID selects
// the current profile, never a caller-selected foreign profile.
type RuleUpdate struct {
	RootID string    `json:"root_id,omitempty"`
	Deny   *[]string `json:"deny_patterns,omitempty"`
	Ignore *[]string `json:"ignore_patterns,omitempty"`
}

type RuleSample struct {
	RootID    string `json:"root_id"`
	Path      string `json:"path"`
	Directory bool   `json:"directory,omitempty"`
}

type RuleDecision struct {
	RuleSample
	Allowed bool `json:"allowed"`
	Ignored bool `json:"ignored"`
}

type RulePreview struct {
	Rules
	Changed             bool           `json:"changed"`
	AffectedConnections []string       `json:"affected_connections"`
	Decisions           []RuleDecision `json:"decisions"`
}

// FileRules reads the local-management view without changing any policy.
func (m *Manager) FileRules(ctx context.Context, connectionID string) (Rules, error) {
	if err := checkContext(ctx); err != nil {
		return Rules{}, err
	}
	snapshot, cfg, connection, profile, err := m.loadTarget(connectionID)
	if err != nil {
		return Rules{}, err
	}
	return rulesFromConfig(snapshot.Revision(), connection, profile, cfg)
}

// PreviewRules uses the actual policy matcher over bounded caller-supplied
// paths. It opens/scans no workspace files and persists nothing. Revision is
// mandatory: the impact preview must describe the same base as a later edit.
func (m *Manager) PreviewRules(ctx context.Context, connectionID, expectedRevision string, update RuleUpdate, samples []RuleSample) (RulePreview, error) {
	if err := checkContext(ctx); err != nil {
		return RulePreview{}, err
	}
	if len(samples) > MaxRuleSamples {
		return RulePreview{}, invalidRules()
	}
	snapshot, cfg, connection, profile, err := m.loadRuleTarget(connectionID, expectedRevision)
	if err != nil {
		return RulePreview{}, err
	}
	next, changed, affected, err := prepareRules(cfg, profile, update)
	if err != nil {
		return RulePreview{}, err
	}
	result, err := previewFromConfig(snapshot.Revision(), next, connection, changed, affected)
	if err != nil {
		return RulePreview{}, err
	}
	if len(samples) == 0 {
		return result, nil
	}
	store, err := config.NewStore(next)
	if err != nil {
		return RulePreview{}, invalidRules()
	}
	manager, err := policy.NewManager(store)
	if err != nil {
		return RulePreview{}, invalidRules()
	}
	bound, err := manager.BindAuthenticated(connection.ID())
	if err != nil {
		return RulePreview{}, problem(CodeInvalidSelection, "路径预览需要已启用的连接。", err)
	}
	for _, sample := range samples {
		if err := checkContext(ctx); err != nil {
			return RulePreview{}, err
		}
		// Preview must not become a policy oracle for another connection's roots.
		if !bound.AllowsRoot(sample.RootID) {
			return RulePreview{}, problem(CodeRootMissing, "只能预览当前连接已启用的 root。", ErrRootMissing)
		}
		if (sample.Path == "" && !sample.Directory) || (sample.Path != "" && !readcore.ValidPath(sample.Path)) {
			return RulePreview{}, invalidRules()
		}
		allowed := bound.AllowsPath(sample.RootID, sample.Path)
		if sample.Directory {
			allowed = bound.AllowsDirectory(sample.RootID, sample.Path)
		}
		result.Decisions = append(result.Decisions, RuleDecision{RuleSample: sample, Allowed: allowed, Ignored: bound.IsIgnoredPath(sample.RootID, sample.Path)})
	}
	return result, nil
}

// SetRules is a LOCAL administrative operation, not a model/MCP tool. The
// caller must present the impact preview to the local user and pass the exact
// affected set after consent. CAS and the existing OS mutation lock protect
// cooperating local writers. This method does NOT claim live runtime reload,
// worker enforcement, or hardened FileStore path/ACL guarantees: adapters must
// separately coordinate revocation before allowing live authorization edits.
func (m *Manager) SetRules(ctx context.Context, connectionID, expectedRevision string, update RuleUpdate, acknowledgedConnections []string) (RulePreview, error) {
	if err := checkContext(ctx); err != nil {
		return RulePreview{}, err
	}
	if len(acknowledgedConnections) > maxWorkspacePaths {
		return RulePreview{}, invalidRules()
	}
	if expectedRevision == "" {
		return RulePreview{}, problem(CodeRevisionRequired, "先读取当前配置 revision。", ErrRevisionRequired)
	}
	release, err := m.acquireMutation(ctx)
	if err != nil {
		return RulePreview{}, err
	}
	defer release()
	snapshot, cfg, connection, profile, err := m.loadRuleTarget(connectionID, expectedRevision)
	if err != nil {
		return RulePreview{}, err
	}
	next, changed, affected, err := prepareRules(cfg, profile, update)
	if err != nil {
		return RulePreview{}, err
	}
	if !exactImpact(acknowledgedConnections, affected) {
		return RulePreview{}, problem(CodeImpactRequired, "先预览并确认全部受影响连接，再提交相同 revision 和连接列表。", ErrImpactRequired)
	}
	if err := checkContext(ctx); err != nil {
		return RulePreview{}, err
	}
	if !changed {
		return previewFromConfig(snapshot.Revision(), cfg, connection, false, affected)
	}
	saved, err := m.store.SaveIfRevision(expectedRevision, next)
	if err != nil {
		return RulePreview{}, mapStoreError(err)
	}
	return previewFromConfig(saved.Revision(), saved.Config(), connection, true, affected)
}

func (m *Manager) loadRuleTarget(connectionID, expected string) (config.Snapshot, config.Config, config.Connection, config.Profile, error) {
	if expected == "" {
		return config.Snapshot{}, config.Config{}, config.Connection{}, config.Profile{}, problem(CodeRevisionRequired, "先读取当前配置 revision。", ErrRevisionRequired)
	}
	snapshot, cfg, connection, profile, err := m.loadTarget(connectionID)
	if err == nil && snapshot.Revision() != expected {
		err = problem(CodeRevisionConflict, "配置已变更，请重新预览。", config.ErrRevisionConflict)
	}
	return snapshot, cfg, connection, profile, err
}

func rulesFromConfig(revision string, connection config.Connection, profile config.Profile, cfg config.Config) (Rules, error) {
	metadata, err := workspaceRootMetadata(profile, cfg)
	if err != nil {
		return Rules{}, mapStoreError(err)
	}
	result := Rules{ConnectionID: connection.ID(), ProfileID: profile.ID(), Revision: revision, Profile: RulePatterns{Deny: profile.DenyPatterns(), Ignore: profile.IgnorePatterns()}, Roots: make([]RootRules, 0, len(metadata))}
	for _, item := range metadata {
		root, ok := cfg.Root(item.RootID())
		if !ok {
			return Rules{}, invalidRules()
		}
		result.Roots = append(result.Roots, RootRules{RootID: root.ID(), Enabled: item.Enabled(), RulePatterns: RulePatterns{Deny: root.DenyPatterns(), Ignore: root.IgnorePatterns()}})
	}
	return result, nil
}

func previewFromConfig(revision string, cfg config.Config, connection config.Connection, changed bool, affected []string) (RulePreview, error) {
	profile, ok := cfg.Profile(connection.ProfileID())
	if !ok {
		return RulePreview{}, invalidRules()
	}
	rules, err := rulesFromConfig(revision, connection, profile, cfg)
	if err != nil {
		return RulePreview{}, err
	}
	return RulePreview{Rules: rules, Changed: changed, AffectedConnections: affected, Decisions: []RuleDecision{}}, nil
}

func prepareRules(cfg config.Config, profile config.Profile, update RuleUpdate) (config.Config, bool, []string, error) {
	if update.Deny == nil && update.Ignore == nil {
		return config.Config{}, false, nil, invalidRules()
	}
	roots, profiles := cfg.Roots(), cfg.Profiles()
	deny, ignore := profile.DenyPatterns(), profile.IgnorePatterns()
	rootIndex := -1
	if update.RootID != "" {
		metadata, err := workspaceRootMetadata(profile, cfg)
		if err != nil {
			return config.Config{}, false, nil, invalidRules()
		}
		registered := false
		for _, item := range metadata {
			registered = registered || item.RootID() == update.RootID
		}
		if !registered {
			return config.Config{}, false, nil, problem(CodeRootMissing, "只能编辑当前连接登记的 root。", ErrRootMissing)
		}
		for i, root := range roots {
			if root.ID() == update.RootID {
				rootIndex = i
				deny, ignore = root.DenyPatterns(), root.IgnorePatterns()
				break
			}
		}
		if rootIndex < 0 {
			return config.Config{}, false, nil, invalidRules()
		}
	}
	nextDeny, nextIgnore := deny, ignore
	if update.Deny != nil {
		nextDeny = slices.Clone(*update.Deny)
	}
	if update.Ignore != nil {
		nextIgnore = slices.Clone(*update.Ignore)
	}
	if !boundedRules(nextDeny, nextIgnore) {
		return config.Config{}, false, nil, invalidRules()
	}
	changed := !slices.Equal(deny, nextDeny) || !slices.Equal(ignore, nextIgnore)
	var err error
	if rootIndex >= 0 {
		root := roots[rootIndex]
		roots[rootIndex], err = config.NewRootWithIgnore(root.ID(), root.Path(), nextDeny, nextIgnore)
	} else {
		profiles[profileIndex(profiles, profile.ID())], err = config.NewProfileWithWorkspaceRoots(profile.ID(), profile.RootIDs(), profile.Tools(), nextDeny, nextIgnore, profile.WorkspaceRoots())
	}
	if err != nil {
		return config.Config{}, false, nil, invalidRules()
	}
	next, err := rebuildConfig(cfg, roots, profiles)
	if err != nil {
		return config.Config{}, false, nil, invalidRules()
	}
	affected := []string{}
	if changed {
		for _, connection := range cfg.Connections() {
			candidate, _ := cfg.Profile(connection.ProfileID())
			impacted := candidate.ID() == profile.ID()
			if update.RootID != "" {
				metadata, err := workspaceRootMetadata(candidate, cfg)
				if err != nil {
					return config.Config{}, false, nil, invalidRules()
				}
				impacted = false
				for _, item := range metadata {
					impacted = impacted || item.RootID() == update.RootID
				}
			}
			if impacted {
				affected = append(affected, connection.ID())
			}
		}
		sort.Strings(affected)
	}
	return next, changed, affected, nil
}

func boundedRules(deny, ignore []string) bool {
	if len(deny)+len(ignore) > MaxRulePatterns {
		return false
	}
	total := 0
	for _, list := range [][]string{deny, ignore} {
		seen := make(map[string]struct{}, len(list))
		for _, pattern := range list {
			total += len(pattern)
			if total > MaxRuleBytes {
				return false
			}
			if _, duplicate := seen[pattern]; duplicate {
				return false
			}
			seen[pattern] = struct{}{}
			for _, r := range pattern {
				if unicode.IsControl(r) {
					return false
				}
			}
		}
	}
	return true
}

func exactImpact(acknowledged, affected []string) bool {
	copyIDs := slices.Clone(acknowledged)
	sort.Strings(copyIDs)
	for i, id := range copyIDs {
		if !validSelectionID(id) || (i > 0 && id == copyIDs[i-1]) {
			return false
		}
	}
	return slices.Equal(copyIDs, affected)
}

func invalidRules() error {
	return problem(CodeInvalidRules, "使用规范相对 glob；deny/ignore 合计最多 256 条、64 KiB，不能包含重复项、控制字符或父目录穿越。", ErrInvalidRules)
}
