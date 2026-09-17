// Package workspaceadmin provides the narrow, local-only configuration
// management surface used by the Preview UI for workspace roots.
//
// It deliberately manages one explicitly selected connection at a time. It
// never accepts a profile id from a model request, never executes a command,
// and never changes credentials or connection metadata. Every mutation uses
// FileStore's revision CAS, so a stale UI cannot overwrite another change.
package workspaceadmin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"

	"github.com/LE-saber/Local-Probe/internal/config"
)

const (
	// DefaultConnectionID is the connection used by the current Preview
	// integration. Callers still have to pass it explicitly to every method;
	// an empty connection id is never treated as "all profiles".
	DefaultConnectionID = "chatgpt-local"

	maxWorkspacePaths = 128
	maxWorkspacePath  = 32768
)

var (
	ErrInvalidManager       = errors.New("invalid workspace manager")
	ErrConnectionMissing    = errors.New("workspace connection not found")
	ErrProfileMissing       = errors.New("workspace profile not found")
	ErrRevisionRequired     = errors.New("workspace configuration revision is required")
	ErrInvalidPath          = errors.New("invalid workspace path")
	ErrPathMissing          = errors.New("workspace path does not exist")
	ErrNotDirectory         = errors.New("workspace path is not a directory")
	ErrPathLink             = errors.New("workspace path is a symlink or reparse point")
	ErrRemotePath           = errors.New("remote or mapped workspace path is not allowed")
	ErrNonFixedDrive        = errors.New("workspace path is not on a fixed local drive")
	ErrDriveRoot            = errors.New("drive root is not allowed as a workspace")
	ErrRootOverlap          = errors.New("workspace roots overlap")
	ErrRootMissing          = errors.New("workspace root is not authorized by this connection")
	ErrRootIDConflict       = errors.New("workspace root id conflicts with another root")
	ErrTooManyPaths         = errors.New("too many workspace paths")
	ErrNoPaths              = errors.New("at least one workspace path is required")
	ErrInvalidRootSelection = errors.New("invalid workspace root selection")
	ErrMutationBusy         = errors.New("workspace configuration is busy")
)

// Code is a stable local-management error category. The UI can use it to
// show a remediation without displaying implementation details.
type Code string

const (
	CodeInvalidManager     Code = "invalid_manager"
	CodeConnectionMissing  Code = "connection_missing"
	CodeProfileMissing     Code = "profile_missing"
	CodeRevisionRequired   Code = "revision_required"
	CodeRevisionConflict   Code = "revision_conflict"
	CodeInvalidPath        Code = "invalid_path"
	CodePathMissing        Code = "path_missing"
	CodeNotDirectory       Code = "not_directory"
	CodePathLink           Code = "symlink_or_reparse"
	CodeRemotePath         Code = "remote_path"
	CodeNonFixedDrive      Code = "non_fixed_drive"
	CodeDriveRoot          Code = "drive_root"
	CodeRootOverlap        Code = "root_overlap"
	CodeRootMissing        Code = "root_not_authorized"
	CodeRootIDConflict     Code = "root_id_conflict"
	CodeTooManyPaths       Code = "too_many_paths"
	CodeNoPaths            Code = "no_paths"
	CodeInvalidSelection   Code = "invalid_selection"
	CodeMutationBusy       Code = "busy"
	CodeConfigInvalid      Code = "config_invalid"
	CodePersistenceFailure Code = "persistence_failure"
)

// Problem is a safe typed error for local UI callers. Path values are not
// included in this error so accidental diagnostics cannot leak a selected
// directory; the successful local projection contains the path explicitly.
type Problem struct {
	Code        Code
	Remediation string
	Cause       error
}

func (p *Problem) Error() string {
	if p == nil {
		return "workspace operation failed"
	}
	if p.Code == "" {
		return "workspace operation failed"
	}
	return string(p.Code)
}

func (p *Problem) Unwrap() error {
	if p == nil {
		return nil
	}
	return p.Cause
}

// ProblemCode returns the stable category for an error, or an empty code for
// an unknown error. Problem remediation text is safe for a local UI.
func ProblemCode(err error) Code {
	var problem *Problem
	if errors.As(err, &problem) && problem != nil {
		return problem.Code
	}
	return ""
}

// Remediation returns the safe user-facing fix, if err is a Problem.
func Remediation(err error) string {
	var problem *Problem
	if errors.As(err, &problem) && problem != nil {
		return problem.Remediation
	}
	return ""
}

// WorkspaceRoot is a root authorized by one selected connection's profile.
// Path is intentionally available because this is a local management UI
// projection, not an MCP/model-facing result.
type WorkspaceRoot struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

// Workspace is the selected connection/profile projection. Roots from any
// other profile are never included.
type Workspace struct {
	ConnectionID string          `json:"connection_id"`
	ProfileID    string          `json:"profile_id"`
	Revision     string          `json:"revision"`
	Roots        []WorkspaceRoot `json:"roots"`
}

// Change is the result of one atomic add/remove operation. Revision is the
// new FileStore revision and Roots is the complete post-change projection for
// the same connection.
type Change struct {
	Workspace
	Changed []WorkspaceRoot `json:"changed,omitempty"`
}

// Manager owns only an explicitly selected FileStore. It does not cache
// configuration, so every operation observes the latest on-disk revision.
type Manager struct {
	store  *config.FileStore
	locker mutationLocker
}

// New creates a workspace manager for one explicit FileStore. The store must
// already point at the intended local-probe.json; this constructor does not
// discover or choose a configuration path.
func New(store *config.FileStore) (*Manager, error) {
	if store == nil {
		return nil, &Problem{Code: CodeInvalidManager, Remediation: "选择一个明确的 local-probe.json 配置文件。", Cause: ErrInvalidManager}
	}
	return &Manager{store: store, locker: newMutationLocker(store.Path())}, nil
}

// List returns only roots authorized by connectionID's profile. A blank id is
// rejected to make accidental all-profile authorization impossible.
func (m *Manager) List(ctx context.Context, connectionID string) (Workspace, error) {
	if err := checkContext(ctx); err != nil {
		return Workspace{}, err
	}
	snapshot, cfg, connection, profile, err := m.loadTarget(connectionID)
	if err != nil {
		return Workspace{}, err
	}
	return workspaceFromConfig(snapshot.Revision(), connection, profile, cfg)
}

// Add validates and authorizes one or more existing ordinary local
// directories for connectionID's current profile. Duplicate paths are
// idempotent, both within paths and against already configured roots.
// expectedRevision is mandatory and is checked again atomically by FileStore.
func (m *Manager) Add(ctx context.Context, connectionID, expectedRevision string, paths []string) (Change, error) {
	if err := checkContext(ctx); err != nil {
		return Change{}, err
	}
	if len(paths) == 0 {
		return Change{}, problem(CodeNoPaths, "选择至少一个文件夹。", ErrNoPaths)
	}
	if len(paths) > maxWorkspacePaths {
		return Change{}, problem(CodeTooManyPaths, "一次最多选择 128 个文件夹。", ErrTooManyPaths)
	}
	if expectedRevision == "" {
		return Change{}, problem(CodeRevisionRequired, "先刷新工作空间列表，再使用当前 revision 保存。", ErrRevisionRequired)
	}
	release, err := m.acquireMutation(ctx)
	if err != nil {
		return Change{}, err
	}
	defer release()

	snapshot, cfg, connection, profile, err := m.loadTarget(connectionID)
	if err != nil {
		return Change{}, err
	}
	if snapshot.Revision() != expectedRevision {
		return Change{}, problem(CodeRevisionConflict, "配置已被其他窗口修改，请刷新后重试。", config.ErrRevisionConflict)
	}

	validated := make([]validatedPath, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, raw := range paths {
		if err := checkContext(ctx); err != nil {
			return Change{}, err
		}
		path, key, err := validateWorkspacePath(raw)
		if err != nil {
			return Change{}, err
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		validated = append(validated, validatedPath{path: path, key: key})
	}
	if err := rejectOverlappingRoots(cfg.Roots(), validated); err != nil {
		return Change{}, err
	}

	roots := cfg.Roots()
	profiles := cfg.Profiles()
	profileIndex := profileIndex(profiles, profile.ID())
	rootIDs := profile.RootIDs()
	rootSet := make(map[string]struct{}, len(rootIDs))
	for _, id := range rootIDs {
		rootSet[id] = struct{}{}
	}
	changed := make([]WorkspaceRoot, 0, len(validated))
	for _, candidate := range validated {
		rootIndex := findRootByPath(roots, candidate.key)
		var root config.Root
		if rootIndex >= 0 {
			root = roots[rootIndex]
		} else {
			id := workspaceRootID(candidate.key)
			if existing, ok := cfg.Root(id); ok && !samePath(existing.Path(), candidate.key) {
				return Change{}, problem(CodeRootIDConflict, "生成的文件夹标识与现有配置冲突，请删除冲突项后重试。", ErrRootIDConflict)
			}
			root, err = config.NewRoot(id, candidate.path, nil)
			if err != nil {
				return Change{}, problem(CodeConfigInvalid, "生成的文件夹配置无效，请重新选择文件夹。", err)
			}
			roots = append(roots, root)
			rootIndex = len(roots) - 1
		}
		if _, authorized := rootSet[root.ID()]; authorized {
			continue
		}
		rootIDs = append(rootIDs, root.ID())
		rootSet[root.ID()] = struct{}{}
		changed = append(changed, WorkspaceRoot{ID: root.ID(), Path: root.Path()})
		_ = rootIndex
	}

	if len(changed) == 0 {
		workspace, err := workspaceFromConfig(snapshot.Revision(), connection, profile, cfg)
		if err != nil {
			return Change{}, err
		}
		return Change{Workspace: workspace}, nil
	}

	updatedProfile, err := cloneProfileWithRoots(profile, rootIDs)
	if err != nil {
		return Change{}, problem(CodeConfigInvalid, "工作空间授权配置无法生成，请检查当前配置。", err)
	}
	profiles[profileIndex] = updatedProfile
	next, err := rebuildConfig(cfg, roots, profiles)
	if err != nil {
		return Change{}, problem(CodeConfigInvalid, "工作空间授权配置无法保存，请检查当前配置。", err)
	}
	saved, err := m.store.SaveIfRevision(expectedRevision, next)
	if err != nil {
		return Change{}, mapStoreError(err)
	}
	updatedConnection, _ := saved.Config().Connection(connection.ID())
	updatedProfile, _ = saved.Config().Profile(profile.ID())
	workspace, err := workspaceFromConfig(saved.Revision(), updatedConnection, updatedProfile, saved.Config())
	if err != nil {
		return Change{}, err
	}
	return Change{Workspace: workspace, Changed: changed}, nil
}

// Remove detaches one or more root IDs from only connectionID's profile.
// A root entity is deleted only when no access profile still references it;
// command-profile references keep the entity so the complete config remains
// valid and all non-workspace fields are preserved.
func (m *Manager) Remove(ctx context.Context, connectionID, expectedRevision string, rootIDs []string) (Change, error) {
	if err := checkContext(ctx); err != nil {
		return Change{}, err
	}
	if len(rootIDs) == 0 {
		return Change{}, problem(CodeInvalidSelection, "选择至少一个已授权文件夹。", ErrInvalidRootSelection)
	}
	if len(rootIDs) > maxWorkspacePaths {
		return Change{}, problem(CodeTooManyPaths, "一次最多选择 128 个文件夹。", ErrTooManyPaths)
	}
	if expectedRevision == "" {
		return Change{}, problem(CodeRevisionRequired, "先刷新工作空间列表，再使用当前 revision 保存。", ErrRevisionRequired)
	}
	release, err := m.acquireMutation(ctx)
	if err != nil {
		return Change{}, err
	}
	defer release()
	snapshot, cfg, connection, profile, err := m.loadTarget(connectionID)
	if err != nil {
		return Change{}, err
	}
	if snapshot.Revision() != expectedRevision {
		return Change{}, problem(CodeRevisionConflict, "配置已被其他窗口修改，请刷新后重试。", config.ErrRevisionConflict)
	}

	selected := make(map[string]struct{}, len(rootIDs))
	for _, id := range rootIDs {
		if !validSelectionID(id) {
			return Change{}, problem(CodeInvalidSelection, "选择中包含无效文件夹标识。", ErrInvalidRootSelection)
		}
		selected[id] = struct{}{}
	}
	currentIDs := profile.RootIDs()
	currentSet := make(map[string]struct{}, len(currentIDs))
	for _, id := range currentIDs {
		currentSet[id] = struct{}{}
	}
	removed := make([]WorkspaceRoot, 0, len(selected))
	for id := range selected {
		if _, ok := currentSet[id]; !ok {
			return Change{}, problem(CodeRootMissing, "只能删除当前 connection 已授权的文件夹。", ErrRootMissing)
		}
		root, ok := cfg.Root(id)
		if !ok {
			return Change{}, problem(CodeConfigInvalid, "当前配置引用了不存在的文件夹。", ErrRootMissing)
		}
		removed = append(removed, WorkspaceRoot{ID: root.ID(), Path: root.Path()})
	}

	remainingIDs := make([]string, 0, len(currentIDs)-len(selected))
	for _, id := range currentIDs {
		if _, remove := selected[id]; !remove {
			remainingIDs = append(remainingIDs, id)
		}
	}
	updatedProfile, err := cloneProfileWithRoots(profile, remainingIDs)
	if err != nil {
		return Change{}, problem(CodeConfigInvalid, "工作空间授权配置无法生成，请检查当前配置。", err)
	}
	profiles := cfg.Profiles()
	profiles[profileIndex(profiles, profile.ID())] = updatedProfile
	roots := cfg.Roots()
	profileReferences := profileRootReferences(profiles)
	commandReferences := commandRootReferences(cfg)
	keptRoots := roots[:0]
	for _, root := range roots {
		if _, remove := selected[root.ID()]; remove {
			if _, used := profileReferences[root.ID()]; !used {
				if _, usedByCommand := commandReferences[root.ID()]; !usedByCommand {
					continue
				}
			}
		}
		keptRoots = append(keptRoots, root)
	}
	roots = keptRoots

	next, err := rebuildConfig(cfg, roots, profiles)
	if err != nil {
		return Change{}, problem(CodeConfigInvalid, "工作空间授权配置无法保存，请检查当前配置。", err)
	}
	saved, err := m.store.SaveIfRevision(expectedRevision, next)
	if err != nil {
		return Change{}, mapStoreError(err)
	}
	updatedConnection, _ := saved.Config().Connection(connection.ID())
	updatedProfile, _ = saved.Config().Profile(profile.ID())
	workspace, err := workspaceFromConfig(saved.Revision(), updatedConnection, updatedProfile, saved.Config())
	if err != nil {
		return Change{}, err
	}
	return Change{Workspace: workspace, Changed: removed}, nil
}

type validatedPath struct {
	path string
	key  string
}

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func (m *Manager) loadTarget(connectionID string) (config.Snapshot, config.Config, config.Connection, config.Profile, error) {
	if m == nil || m.store == nil {
		return config.Snapshot{}, config.Config{}, config.Connection{}, config.Profile{}, &Problem{Code: CodeInvalidManager, Remediation: "选择一个明确的 local-probe.json 配置文件。", Cause: ErrInvalidManager}
	}
	if !validSelectionID(connectionID) {
		return config.Snapshot{}, config.Config{}, config.Connection{}, config.Profile{}, problem(CodeConnectionMissing, "确认连接标识为 chatgpt-local 或其他已配置 connection。", ErrConnectionMissing)
	}
	snapshot, err := m.store.Load()
	if err != nil {
		return config.Snapshot{}, config.Config{}, config.Connection{}, config.Profile{}, mapStoreError(err)
	}
	cfg := snapshot.Config()
	connection, ok := cfg.Connection(connectionID)
	if !ok {
		return config.Snapshot{}, config.Config{}, config.Connection{}, config.Profile{}, problem(CodeConnectionMissing, "确认该 connection 已在 local-probe.json 中配置。", ErrConnectionMissing)
	}
	profile, ok := cfg.Profile(connection.ProfileID())
	if !ok {
		return config.Snapshot{}, config.Config{}, config.Connection{}, config.Profile{}, problem(CodeProfileMissing, "修复 connection 的 profile_id，使其指向已存在的 profile。", ErrProfileMissing)
	}
	return snapshot, cfg, connection, profile, nil
}

func (m *Manager) acquireMutation(ctx context.Context) (func(), error) {
	if m == nil || m.locker == nil {
		return nil, problem(CodeInvalidManager, "选择一个明确的 local-probe.json 配置文件。", ErrInvalidManager)
	}
	release, err := m.locker.acquire(ctx)
	if err == nil {
		return release, nil
	}
	if errors.Is(err, ErrMutationBusy) {
		return nil, problem(CodeMutationBusy, "已有窗口正在修改工作空间配置，请稍后重试。", ErrMutationBusy)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return nil, problem(CodeMutationBusy, "已有窗口正在修改工作空间配置，请稍后重试。", ErrMutationBusy)
	}
	if errors.Is(err, context.Canceled) {
		return nil, err
	}
	return nil, err
}

func workspaceFromConfig(revision string, connection config.Connection, profile config.Profile, cfg config.Config) (Workspace, error) {
	workspace := Workspace{ConnectionID: connection.ID(), ProfileID: profile.ID(), Revision: revision, Roots: make([]WorkspaceRoot, 0, len(profile.RootIDs()))}
	for _, id := range profile.RootIDs() {
		root, ok := cfg.Root(id)
		if !ok {
			return Workspace{}, problem(CodeConfigInvalid, "当前 profile 引用了不存在的 root，请修复配置后重试。", ErrProfileMissing)
		}
		workspace.Roots = append(workspace.Roots, WorkspaceRoot{ID: root.ID(), Path: root.Path()})
	}
	return workspace, nil
}

func profileIndex(profiles []config.Profile, id string) int {
	for i, profile := range profiles {
		if profile.ID() == id {
			return i
		}
	}
	return -1
}

func cloneProfileWithRoots(profile config.Profile, rootIDs []string) (config.Profile, error) {
	return config.NewProfileWithIgnore(profile.ID(), rootIDs, profile.Tools(), profile.DenyPatterns(), profile.IgnorePatterns())
}

func rebuildConfig(cfg config.Config, roots []config.Root, profiles []config.Profile) (config.Config, error) {
	return config.NewWithCommandProfiles(
		cfg.SchemaVersion(), roots, profiles, cfg.Connections(), cfg.Credentials(),
		cfg.EnvironmentTools(), cfg.DeveloperMode(), cfg.CommandProfiles(),
	)
}

func findRootByPath(roots []config.Root, key string) int {
	for i, root := range roots {
		if samePath(root.Path(), key) {
			return i
		}
	}
	return -1
}

func rejectOverlappingRoots(roots []config.Root, candidates []validatedPath) error {
	for _, candidate := range candidates {
		for _, root := range roots {
			existingKey := pathComparisonKey(root.Path())
			if samePath(existingKey, candidate.key) {
				continue
			}
			if pathsOverlap(existingKey, candidate.key) {
				return problem(CodeRootOverlap, "新增文件夹不能与已有工作空间存在父子重叠；请只保留一个授权范围，避免绕过原有拒绝规则。", ErrRootOverlap)
			}
		}
	}
	for i := 0; i < len(candidates); i++ {
		for j := i + 1; j < len(candidates); j++ {
			if pathsOverlap(candidates[i].key, candidates[j].key) {
				return problem(CodeRootOverlap, "一次选择的文件夹存在父子重叠；请只保留一个授权范围。", ErrRootOverlap)
			}
		}
	}
	return nil
}

func pathsOverlap(left, right string) bool {
	left = pathComparisonKey(left)
	right = pathComparisonKey(right)
	if left == right {
		return true
	}
	return pathContains(left, right) || pathContains(right, left)
}

func pathContains(parent, child string) bool {
	parent = strings.TrimRight(parent, `/\`)
	child = strings.TrimRight(child, `/\`)
	if parent == "" || child == "" {
		return false
	}
	if parent == string(filepath.Separator) {
		return strings.HasPrefix(child, parent)
	}
	separator := string(filepath.Separator)
	if isCaseInsensitiveFS() {
		parent = strings.ToLower(parent)
		child = strings.ToLower(child)
	}
	return strings.HasPrefix(child, parent+separator)
}

func pathComparisonKey(path string) string {
	path = filepath.Clean(path)
	if isCaseInsensitiveFS() {
		path = strings.ToLower(path)
	}
	return path
}

func profileRootReferences(profiles []config.Profile) map[string]struct{} {
	refs := make(map[string]struct{})
	for _, profile := range profiles {
		for _, id := range profile.RootIDs() {
			refs[id] = struct{}{}
		}
	}
	return refs
}

func commandRootReferences(cfg config.Config) map[string]struct{} {
	refs := make(map[string]struct{})
	for _, profile := range cfg.CommandProfiles() {
		for _, slot := range profile.Slots() {
			for _, id := range slot.RootIDs() {
				refs[id] = struct{}{}
			}
		}
	}
	return refs
}

func workspaceRootID(pathKey string) string {
	digest := sha256.Sum256([]byte(pathKey))
	return "workspace-" + hex.EncodeToString(digest[:])
}

func validSelectionID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func problem(code Code, remediation string, cause error) error {
	return &Problem{Code: code, Remediation: remediation, Cause: cause}
}

func mapStoreError(err error) error {
	if errors.Is(err, config.ErrRevisionConflict) {
		return problem(CodeRevisionConflict, "配置已被其他窗口修改，请刷新后重试。", err)
	}
	if errors.Is(err, config.ErrInvalid) {
		return problem(CodeConfigInvalid, "检查 local-probe.json 的结构和引用后重试。", err)
	}
	return problem(CodePersistenceFailure, "检查配置文件目录权限和磁盘状态后重试；已有配置不会被覆盖。", err)
}

// normalizePath is implemented in path_safety_*.go so Windows can reject
// mapped drives and reparse points without weakening non-Windows builds.
func samePath(left, right string) bool {
	left = pathComparisonKey(left)
	right = pathComparisonKey(right)
	return left == right
}

// validateWorkspacePath returns a cleaned absolute path and its stable
// comparison key. It performs only filesystem metadata checks; no command or
// user-supplied executable is ever launched.
func validateWorkspacePath(raw string) (string, string, error) {
	return validateExistingWorkspaceDirectory(raw)
}
