package desktopbridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/LE-saber/Local-Probe/internal/commandprofile"
	"github.com/LE-saber/Local-Probe/internal/config"
	"github.com/LE-saber/Local-Probe/internal/previewconnect"
	"github.com/LE-saber/Local-Probe/internal/workspaceadmin"
)

const GUIVersion = "R11.3-desktop"

type SavedConnection struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	ProfileID string `json:"profile_id"`
	Transport string `json:"transport"`
	Port      int    `json:"port"`
	Enabled   bool   `json:"enabled"`
}
type ManagementEvent struct {
	Timestamp    time.Time `json:"timestamp"`
	EventType    string    `json:"event_type"`
	ConnectionID string    `json:"connection_id"`
	Transport    string    `json:"transport"`
	RootIDs      []string  `json:"root_ids,omitempty"`
	Outcome      string    `json:"outcome"`
	ErrorCode    string    `json:"error_code,omitempty"`
}
type DesktopProjection struct {
	Developer       DeveloperProjection `json:"developer"`
	RootRules       []RootRule          `json:"root_rules"`
	Version         string              `json:"version"`
	Revision        string              `json:"revision"`
	ActiveID        string              `json:"active_id"`
	Connections     []SavedConnection   `json:"connections"`
	Events          []ManagementEvent   `json:"events"`
	EventsAvailable bool                `json:"events_available"`
}

type RootRule struct {
	RootID         string   `json:"root_id"`
	DenyPatterns   []string `json:"deny_patterns"`
	IgnorePatterns []string `json:"ignore_patterns"`
}

func (s *Service) targetID() string { s.mu.Lock(); defer s.mu.Unlock(); return s.activeID }

func desktopTransport(connection config.Connection, fallback config.ConnectionTransport) config.ConnectionTransport {
	if allowedTransport(connection.DesktopTransport()) {
		return connection.DesktopTransport()
	}
	if allowedTransport(connection.Transport()) {
		return connection.Transport()
	}
	return fallback
}
func (s *Service) loadDesktopSelection() {
	snapshot, err := s.store.Load()
	if err != nil {
		return
	}
	cfg := snapshot.Config()
	id := cfg.DesktopConnectionID()
	if id == "" {
		id = workspaceadmin.DefaultConnectionID
	}
	if _, ok := cfg.Connection(id); !ok && len(cfg.Connections()) > 0 {
		id = cfg.Connections()[0].ID()
	}
	s.activeID = id
	if connection, ok := cfg.Connection(id); ok {
		s.transport = desktopTransport(connection, s.transport)
		s.status.Transport = s.transport
	}
}
func (s *Service) desktopProjection() DesktopProjection {
	s.mu.Lock()
	p := DesktopProjection{Version: GUIVersion, ActiveID: s.activeID, Connections: []SavedConnection{}, Events: append([]ManagementEvent(nil), s.events...), EventsAvailable: s.eventsHealthy}
	fallback := s.transport
	s.mu.Unlock()
	if snapshot, err := s.store.Load(); err == nil {
		p.Revision = snapshot.Revision()
		cfg := snapshot.Config()
		p.Developer = projectDeveloper(cfg)
		if c, ok := cfg.Connection(p.ActiveID); ok {
			if profile, ok := cfg.Profile(c.ProfileID()); ok {
				ids := profile.RootIDs()
				for _, m := range profile.WorkspaceRoots() {
					if !m.Enabled() {
						ids = append(ids, m.RootID())
					}
				}
				for _, id := range ids {
					if len(p.RootRules) >= 128 {
						break
					}
					if root, ok := cfg.Root(id); ok {
						p.RootRules = append(p.RootRules, RootRule{RootID: id, DenyPatterns: append(root.DenyPatterns(), profile.DenyPatterns()...), IgnorePatterns: append(root.IgnorePatterns(), profile.IgnorePatterns()...)})
					}
				}
			}
		}
		for _, c := range snapshot.Config().Connections() {
			if len(p.Connections) >= 128 {
				break
			}
			transport := desktopTransport(c, fallback)
			port := c.DesktopPort()
			if port == 0 {
				port = 8788
				if transport == config.TransportOpenAIRuntime {
					port = 8787
				}
			}
			p.Connections = append(p.Connections, SavedConnection{c.ID(), c.Label(), c.ProfileID(), string(transport), port, c.Enabled()})
		}
	}
	return p
}

// All management writes share the lifecycle operation guard. Stopping before
// changing ingress/profile bindings avoids applying new authority to an old
// child. A failed CAS leaves the child stopped rather than guessing a rollback.
func (s *Service) desktopMutation(method string, params json.RawMessage) (Snapshot, *rpcError) {
	var expected, id, name, transport, selectionID string
	var port int
	var developer developerRequest
	switch method {
	case "developer.configure", "developer.saveProfile", "developer.removeProfile":
		if !decodeDeveloperRequest(method, params, &developer) {
			return s.snapshot(), errorFor("invalid_params")
		}
		expected = developer.ExpectedRevision
	case "connections.save":
		var p struct {
			ExpectedRevision string `json:"expected_revision"`
			ID               string `json:"id"`
			Name             string `json:"name"`
			Transport        string `json:"transport"`
			Port             int    `json:"port"`
		}
		if !decodeParams(params, &p) || strings.TrimSpace(p.Name) == "" || len(p.Name) > 120 || !utf8.ValidString(p.Name) || strings.ContainsAny(p.Name, "\r\n\x00") || !allowedTransport(config.ConnectionTransport(p.Transport)) || p.Port < 1024 || p.Port > 65535 {
			return s.snapshot(), errorFor("invalid_params")
		}
		expected, id, name, transport, port = p.ExpectedRevision, p.ID, strings.TrimSpace(p.Name), p.Transport, p.Port
	case "connections.select", "connections.remove":
		var p struct {
			ExpectedRevision string `json:"expected_revision"`
			ID               string `json:"id"`
		}
		if !decodeParams(params, &p) || p.ExpectedRevision == "" || p.ID == "" {
			return s.snapshot(), errorFor("invalid_params")
		}
		expected, id = p.ExpectedRevision, p.ID
	case "settings.backup":
		var p struct {
			ExpectedRevision string `json:"expected_revision"`
		}
		if !decodeParams(params, &p) || p.ExpectedRevision == "" {
			return s.snapshot(), errorFor("invalid_params")
		}
		expected = p.ExpectedRevision
	case "settings.restore":
		var p struct {
			ExpectedRevision string `json:"expected_revision"`
			SelectionID      string `json:"selection_id"`
		}
		if !decodeParams(params, &p) || p.SelectionID == "" {
			return s.snapshot(), errorFor("invalid_params")
		}
		expected, selectionID = p.ExpectedRevision, p.SelectionID
	}
	err := s.startAsync(method, func(ctx context.Context, _generation uint64) operationResult {
		if ctx.Err() != nil {
			return operationResult{code: "cancelled", saved: boolPtr(false), applied: boolPtr(false)}
		}
		snapshot, loadErr := s.store.Load()
		var cfg config.Config
		if loadErr != nil {
			if !errors.Is(loadErr, config.ErrConfigNotFound) || (method != "connections.save" && method != "settings.restore") || expected != "" || id != "" {
				return operationResult{code: workspaceErrorCode(loadErr), saved: boolPtr(false), applied: boolPtr(false)}
			}
			cfg, loadErr = config.New(config.SchemaVersionV1, nil, nil, nil, nil)
		} else {
			cfg = snapshot.Config()
			if snapshot.Revision() != expected {
				return operationResult{code: "revision_conflict", saved: boolPtr(false), applied: boolPtr(false)}
			}
		}
		if loadErr != nil {
			return operationResult{code: "config_invalid", saved: boolPtr(false), applied: boolPtr(false)}
		}
		active := s.targetID()
		if method == "connections.select" || method == "connections.remove" || method == "connections.save" && id != "" {
			if _, ok := cfg.Connection(id); !ok {
				return operationResult{code: "connection_missing", saved: boolPtr(false), applied: boolPtr(false)}
			}
		}
		if method == "settings.backup" {
			if s.options.SaveBackup == nil {
				return operationResult{code: "backup_unavailable"}
			}
			data, err := EncodeBackup(cfg, time.Now().UTC())
			if err == nil && ctx.Err() == nil {
				err = s.options.SaveBackup(ctx, data)
			}
			if errors.Is(err, ErrBackupCancelled) || ctx.Err() != nil {
				return operationResult{code: "cancelled"}
			}
			if err != nil {
				return operationResult{code: "backup_failed"}
			}
			return operationResult{}
		}
		var next config.Config
		var err error
		switch method {
		case "developer.configure", "developer.saveProfile", "developer.removeProfile":
			next, err = mutateDeveloper(cfg, method, developer)
		case "connections.save":
			connections, profiles, credentials := cfg.Connections(), cfg.Profiles(), cfg.Credentials()
			if id == "" {
				if len(connections) >= 128 {
					return operationResult{code: "connection_limit", saved: boolPtr(false)}
				}
				var random [12]byte
				if _, err = rand.Read(random[:]); err != nil {
					return operationResult{code: "internal_error", saved: boolPtr(false)}
				}
				id = "desktop-" + hex.EncodeToString(random[:])
				if len(connections) == 0 {
					id = workspaceadmin.DefaultConnectionID
				}
				profile, profileErr := config.NewProfile("scope-"+hex.EncodeToString(random[:]), nil, []string{"server_info", "ping", "read_file", "batch_read", "list_directory", "find_files", "search_text", "tree_directory", "get_environment", "discover_tools", "workspace_snapshot"}, nil)
				if profileErr != nil {
					return operationResult{code: "config_invalid", saved: boolPtr(false)}
				}
				profiles = append(profiles, profile)
				credentialID := "desktop-hop"
				if len(credentials) == 0 {
					credentials = append(credentials, config.NewCredentialRef(credentialID, "local_bearer"))
				} else {
					credentialID = credentials[0].ID()
				}
				connections = append(connections, config.NewConnection(id, name, profile.ID(), credentialID, true).WithDesktop(name, config.ConnectionTransport(transport), port))
				active = id
			} else {
				for i, c := range connections {
					if c.ID() == id {
						connections[i] = c.WithDesktop(name, config.ConnectionTransport(transport), port)
					}
				}
			}
			next, err = config.NewWithCommandProfiles(cfg.SchemaVersion(), cfg.Roots(), profiles, connections, credentials, cfg.EnvironmentTools(), cfg.DeveloperMode(), cfg.CommandProfiles())
		case "connections.select":
			active = id
			next = cfg
		case "connections.remove":
			connections := []config.Connection{}
			for _, c := range cfg.Connections() {
				if c.ID() != id {
					connections = append(connections, c)
				}
			}
			if active == id {
				active = ""
				if len(connections) > 0 {
					active = connections[0].ID()
				}
			}
			allowed := []string{}
			for _, connectionID := range cfg.DeveloperMode().AllowedConnections() {
				if connectionID != id {
					allowed = append(allowed, connectionID)
				}
			}
			mode, modeErr := commandprofile.NewDeveloperMode(cfg.DeveloperMode().Enabled() && len(allowed) > 0, allowed, commandprofile.ConfirmationPerCall, commandprofile.NetworkDeny)
			if modeErr != nil {
				return operationResult{code: "config_invalid", saved: boolPtr(false)}
			}
			next, err = config.NewWithCommandProfiles(cfg.SchemaVersion(), cfg.Roots(), cfg.Profiles(), connections, cfg.Credentials(), cfg.EnvironmentTools(), mode, cfg.CommandProfiles())
		case "settings.restore":
			s.mu.Lock()
			candidate := s.backupCandidate
			if candidate == nil || candidate.id != selectionID || candidate.revision != expected || time.Now().After(candidate.expires) {
				s.mu.Unlock()
				return operationResult{code: "backup_selection_expired", saved: boolPtr(false)}
			}
			s.backupCandidate = nil // one-use validated snapshot, never reread a changed file
			s.mu.Unlock()
			backupCfg := candidate.config
			profiles := []config.Profile{}
			// Restored roots remain paused; no command permissions are restored.
			for _, p := range backupCfg.Profiles() {
				metadata := []config.WorkspaceRootMetadata{}
				seen := map[string]bool{}
				for _, m := range p.WorkspaceRoots() {
					paused, _ := config.NewWorkspaceRootMetadata(m.RootID(), m.DisplayName(), false)
					metadata = append(metadata, paused)
					seen[m.RootID()] = true
				}
				for _, rootID := range p.RootIDs() {
					if !seen[rootID] {
						root, _ := backupCfg.Root(rootID)
						paused, _ := config.NewWorkspaceRootMetadata(rootID, filepath.Base(root.Path()), false)
						metadata = append(metadata, paused)
					}
				}
				profile, profileErr := config.NewProfileWithWorkspaceRoots(p.ID(), nil, p.Tools(), p.DenyPatterns(), p.IgnorePatterns(), metadata)
				if profileErr != nil {
					return operationResult{code: "config_invalid", saved: boolPtr(false)}
				}
				profiles = append(profiles, profile)
			}
			next, err = config.NewWithEnvironmentTools(backupCfg.SchemaVersion(), backupCfg.Roots(), profiles, backupCfg.Connections(), backupCfg.Credentials(), backupCfg.EnvironmentTools())
			active = backupCfg.DesktopConnectionID()
			if active == "" && len(backupCfg.Connections()) > 0 {
				active = backupCfg.Connections()[0].ID()
			}
		}
		if err != nil {
			return operationResult{code: "config_invalid", saved: boolPtr(false), applied: boolPtr(false)}
		}
		next, err = next.WithDesktopConnectionID(active)
		if err != nil {
			return operationResult{code: "config_invalid", saved: boolPtr(false), applied: boolPtr(false)}
		}
		s.mu.Lock()
		old := s.controller
		s.mu.Unlock()
		if old != nil {
			if err := old.Stop(); err != nil {
				status := old.Status()
				return operationResult{status: &status, code: "stop_failed", saved: boolPtr(false), applied: boolPtr(false)}
			}
		}
		if ctx.Err() != nil {
			return operationResult{code: "cancelled", saved: boolPtr(false), applied: boolPtr(false)}
		}
		s.mu.Lock()
		s.controller = nil
		s.statusKnown = false
		s.status = previewconnect.Status{Stage: previewconnect.StageIdle, Transport: s.transport}
		s.mu.Unlock()
		if _, err = s.store.SaveIfRevision(expected, next); err != nil {
			return operationResult{code: workspaceErrorCode(err), saved: boolPtr(false), applied: boolPtr(false)}
		}
		s.mu.Lock()
		s.activeID = active
		if c, ok := next.Connection(active); ok {
			s.transport = desktopTransport(c, s.transport)
		}
		status := previewconnect.Status{Stage: previewconnect.StageIdle, Transport: s.transport, Message: "配置已保存；连接保持停止。", Remedy: "确认授权目录后显式连接。"}
		s.mu.Unlock()
		return operationResult{status: &status, saved: boolPtr(true), applied: boolPtr(false)}
	})
	if err != nil {
		return s.snapshot(), errorFor(codeFor(err))
	}
	return s.snapshot(), nil
}

// Management history is bounded, path/credential-free and kept separate from
// the runtime audit. Old records without scope metadata are never guessed.
func (s *Service) loadEvents() {
	s.eventsHealthy = true
	file, err := os.Open(s.options.ConfigPath + ".desktop-events.json")
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		s.eventsHealthy = false
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil || len(data) > 65536 || json.Unmarshal(data, &s.events) != nil || len(s.events) > 128 {
		s.events = nil
		s.eventsHealthy = false
		return
	}
	for _, e := range s.events {
		if !validManagementEvent(e) {
			s.events = nil
			s.eventsHealthy = false
			return
		}
	}
}
func validManagementEvent(e ManagementEvent) bool {
	if e.Timestamp.IsZero() || len(e.RootIDs) > 128 || !eventIdentifier.MatchString(e.ConnectionID) || e.EventType == "" || !eventIdentifier.MatchString(e.EventType) || !eventIdentifier.MatchString(e.ErrorCode) || !allowedTransport(config.ConnectionTransport(e.Transport)) || (e.Outcome != "success" && e.Outcome != "failure") {
		return false
	}
	for _, id := range e.RootIDs {
		if id == "" || !eventIdentifier.MatchString(id) {
			return false
		}
	}
	return true
}

var eventIdentifier = regexp.MustCompile(`^[A-Za-z0-9._-]{0,128}$`)

func (s *Service) recordEventLocked(name, code string, rootIDs []string) {
	event := ManagementEvent{Timestamp: time.Now().UTC(), EventType: name, ConnectionID: s.activeID, Transport: string(s.transport), RootIDs: append([]string(nil), rootIDs...), Outcome: "success", ErrorCode: code}
	if code != "" {
		event.Outcome = "failure"
	}
	if !validManagementEvent(event) {
		s.eventsHealthy = false
		return
	}
	s.events = append(s.events, event)
	if len(s.events) > 128 {
		s.events = s.events[len(s.events)-128:]
	}
	data, err := json.Marshal(s.events)
	// Also cap bytes, not only record count: a large workspace event can carry
	// many root IDs. Discard oldest records rather than losing persistence.
	for err == nil && len(data) > 65536 && len(s.events) > 1 {
		s.events = s.events[1:]
		data, err = json.Marshal(s.events)
	}
	if err != nil || len(data) > 65536 {
		s.eventsHealthy = false
		return
	}
	temporary, err := os.CreateTemp(filepath.Dir(s.options.ConfigPath), ".desktop-events-*")
	if err != nil {
		s.eventsHealthy = false
		return
	}
	path := temporary.Name()
	defer os.Remove(path)
	if temporary.Chmod(0600) != nil {
		temporary.Close()
		s.eventsHealthy = false
		return
	}
	_, err = temporary.Write(data)
	if err == nil {
		err = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = replaceDesktopFile(path, s.options.ConfigPath+".desktop-events.json")
	}
	s.eventsHealthy = err == nil
}
