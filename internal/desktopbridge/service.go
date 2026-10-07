// Package desktopbridge exposes a typed, local-only JSON bridge for the
// desktop preview. It is not a shell, file, environment, or process API.
package desktopbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/LE-saber/Local-Probe/internal/config"
	"github.com/LE-saber/Local-Probe/internal/previewapp"
	"github.com/LE-saber/Local-Probe/internal/previewconnect"
	"github.com/LE-saber/Local-Probe/internal/previewui"
	"github.com/LE-saber/Local-Probe/internal/workspaceadmin"
)

const (
	maxRequestBytes   = 64 << 10
	maxResponseBytes  = 2 << 20
	maxWorkspaceBytes = 256 << 10
	operationTimeout  = 2 * time.Minute
)

var (
	ErrInvalidOptions          = errors.New("invalid desktop bridge options")
	ErrClosed                  = errors.New("desktop bridge is closed")
	ErrBusy                    = errors.New("desktop bridge operation is busy")
	ErrCloseStopFailed         = errors.New("owned connection could not be stopped")
	ErrPickerUnavailable       = errors.New("desktop bridge folder picker unavailable")
	ErrPickerFailed            = errors.New("desktop bridge folder picker failed")
	ErrPickerSelectionTooLarge = errors.New("desktop bridge picker selection exceeds limit")
	ErrExportUnavailable       = errors.New("desktop bridge export unavailable")
	ErrExportFailed            = errors.New("desktop bridge export failed")
	ErrExportRejected          = errors.New("desktop bridge export rejected")
	ErrExportTooLarge          = errors.New("desktop bridge export exceeds limit")
)

// ExportKind is the closed set of local exports supported by the bridge.
type ExportKind string

const (
	ExportLogs        ExportKind = "logs"
	ExportDiagnostics ExportKind = "diagnostics"
)

// FolderPicker opens a host-owned native folder dialog. Returned paths are
// only suggestions until workspace.add passes them through workspaceadmin.
type FolderPicker func(context.Context) ([]string, error)

// ExportCallback receives only bounded, already projected JSON. The host
// chooses a local destination; no destination path is accepted over RPC.
type ExportCallback func(context.Context, ExportKind, []byte) error

// Options contains the trusted local composition inputs. Paths are provided
// by the desktop host and are never accepted from RPC parameters.
type Options struct {
	ConfigPath string
	AuditDir   string
	RepoRoot   string
	Transport  string

	PickFolders FolderPicker
	Export      ExportCallback
	// Host-owned dialogs. No backup paths or file bodies are accepted over RPC.
	SaveBackup func(context.Context, []byte) error
	LoadBackup func(context.Context) ([]byte, error)
}

// ConnectionProjection contains only bounded local lifecycle evidence. It
// deliberately excludes public hosts, credential hints, and process output.
type ConnectionProjection struct {
	Transport       string     `json:"transport"`
	Stage           string     `json:"stage"`
	Code            string     `json:"code"`
	Busy            bool       `json:"busy"`
	Generation      uint64     `json:"generation"`
	Operation       string     `json:"operation,omitempty"`
	StatusKnown     bool       `json:"status_known"`
	MCPReady        bool       `json:"mcp_ready"`
	TunnelReady     bool       `json:"tunnel_ready"`
	TokenConfigured bool       `json:"token_configured"`
	HealthScope     string     `json:"health_scope"`
	CheckedAt       *time.Time `json:"checked_at,omitempty"`
	ObservedAt      time.Time  `json:"observed_at"`
	Message         string     `json:"message"`
	Remedy          string     `json:"remedy"`
	ChatGPTStatus   string     `json:"chatgpt_status"`
	Saved           *bool      `json:"saved,omitempty"`
	Applied         *bool      `json:"applied,omitempty"`
}

// WorkspaceProjection is a local management view. Its paths are not included
// in the path-free previewui.Snapshot or in either export format.
type WorkspaceProjection struct {
	Available    bool                           `json:"available"`
	ConnectionID string                         `json:"connection_id,omitempty"`
	ProfileID    string                         `json:"profile_id,omitempty"`
	Revision     string                         `json:"revision,omitempty"`
	Roots        []workspaceadmin.WorkspaceRoot `json:"roots"`
	Truncated    bool                           `json:"truncated"`
	ErrorCode    string                         `json:"error_code,omitempty"`
}

// Snapshot is the one data object consumed by every page and poller.
type Snapshot struct {
	FileRules       *workspaceadmin.Rules       `json:"file_rules,omitempty"`
	RulePreview     *workspaceadmin.RulePreview `json:"rule_preview,omitempty"`
	BackupSelection *BackupSelection            `json:"backup_selection,omitempty"`
	Desktop         DesktopProjection           `json:"desktop"`
	Connection      ConnectionProjection        `json:"connection"`
	Workspace       WorkspaceProjection         `json:"workspace"`
	View            previewui.Snapshot          `json:"view"`
	Paths           []string                    `json:"paths,omitempty"`
}

type rpcRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	ID    json.RawMessage `json:"id"`
	OK    bool            `json:"ok"`
	Data  *Snapshot       `json:"data,omitempty"`
	Error *rpcError       `json:"error,omitempty"`
}

type lifecycleController interface {
	Connect(context.Context, previewconnect.ProgressFunc) (previewconnect.Status, error)
	Reconnect(context.Context, previewconnect.ProgressFunc) (previewconnect.Status, error)
	Stop() error
	Status() previewconnect.Status
}

type operationResult struct {
	rootIDs []string
	status  *previewconnect.Status
	code    string
	saved   *bool
	applied *bool
}

// Service owns only the controller instances it creates. Developer RPC edits
// strictly validated, inert configuration; it never dispatches a process or
// accepts an arbitrary file-read, environment or shell-execution request.
type Service struct {
	closeMu sync.Mutex
	mu      sync.Mutex

	app             *previewapp.App
	uiModel         *previewui.PreviewAppModel
	workspaces      *workspaceadmin.Manager
	store           *config.FileStore
	activeID        string
	events          []ManagementEvent
	eventsHealthy   bool
	backupCandidate *backupCandidate
	nativeBusy      bool
	options         Options

	controller        lifecycleController
	controllerFactory func(config.ConnectionTransport) (lifecycleController, error)
	transport         config.ConnectionTransport
	status            previewconnect.Status
	statusKnown       bool

	busy            bool
	cancelRequested bool
	cancel          context.CancelFunc
	opDone          chan struct{}
	generation      uint64
	operation       string
	operationCode   string
	saved           *bool
	applied         *bool
	closed          bool
	closeComplete   bool
	appClosed       bool
}

// New creates the typed local service. A missing ConfigPath file is a valid
// first-run state; only its explicitly supplied parent directory may be
// created. No configuration, authorization, or credential is generated.
func New(options Options) (*Service, error) {
	if !validAbsolutePath(options.ConfigPath) || !validAbsolutePath(options.RepoRoot) ||
		(options.AuditDir != "" && !validAbsolutePath(options.AuditDir)) {
		return nil, ErrInvalidOptions
	}
	transport := config.ConnectionTransport(strings.TrimSpace(options.Transport))
	if transport == "" {
		transport = config.TransportCloudflareNamed
	}
	if !allowedTransport(transport) {
		return nil, ErrInvalidOptions
	}
	options.ConfigPath = filepath.Clean(options.ConfigPath)
	options.RepoRoot = filepath.Clean(options.RepoRoot)
	if options.AuditDir != "" {
		options.AuditDir = filepath.Clean(options.AuditDir)
	}

	store, err := config.NewFileStoreWithOptions(config.FileStoreOptions{Path: options.ConfigPath, CreateParent: true})
	if err != nil {
		return nil, ErrInvalidOptions
	}
	workspaces, err := workspaceadmin.New(store)
	if err != nil {
		return nil, ErrInvalidOptions
	}
	app, err := previewapp.New(previewapp.Options{ConfigPath: options.ConfigPath, AuditDir: options.AuditDir})
	if err != nil {
		return nil, ErrInvalidOptions
	}
	service := &Service{
		app:        app,
		uiModel:    previewui.NewPreviewAppModel(app),
		workspaces: workspaces,
		store:      store,
		activeID:   workspaceadmin.DefaultConnectionID,
		options:    options,
		transport:  transport,
		status: previewconnect.Status{
			Stage:     previewconnect.StageIdle,
			Transport: transport,
			Message:   "尚未执行本机连接检查。",
			Remedy:    "连接状态只表示本机启动检查与自有进程状态。",
		},
	}
	service.controllerFactory = service.newController
	service.loadDesktopSelection()
	service.loadEvents()
	return service, nil
}

// Handle validates one bounded RPC request and always returns a safe JSON
// envelope. Panics and operating-system errors never cross this boundary.
func (s *Service) Handle(raw string) (response string) {
	id := json.RawMessage("null")
	defer func() {
		if recover() != nil {
			response = encodeResponse(rpcResponse{ID: id, OK: false, Error: errorFor("internal_error")})
		}
	}()
	if s == nil || len(raw) == 0 || len(raw) > maxRequestBytes || !utf8.ValidString(raw) {
		return encodeResponse(rpcResponse{ID: id, OK: false, Error: errorFor("invalid_request")})
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var request rpcRequest
	if err := decoder.Decode(&request); err != nil || ensureEOF(decoder) != nil ||
		request.ID == nil || !validRPCID(request.ID) || request.Method == "" || len(request.Method) > 64 {
		return encodeResponse(rpcResponse{ID: id, OK: false, Error: errorFor("invalid_request")})
	}
	id = append(json.RawMessage(nil), request.ID...)
	if len(request.Params) == 0 {
		request.Params = json.RawMessage(`{}`)
	}
	if len(request.Params) > maxRequestBytes || !jsonObject(request.Params) {
		return encodeResponse(rpcResponse{ID: id, OK: false, Error: errorFor("invalid_params")})
	}

	data, rpcErr := s.dispatch(request.Method, request.Params)
	if rpcErr != nil {
		return encodeResponse(rpcResponse{ID: id, OK: false, Data: &data, Error: rpcErr})
	}
	return encodeResponse(rpcResponse{ID: id, OK: true, Data: &data})
}

// Close cancels in-flight work, waits for it to finish, stops only this
// service's controller, and closes the read-only preview model.
func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	s.closeMu.Lock()
	defer s.closeMu.Unlock()

	s.mu.Lock()
	if s.closeComplete {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	cancel := s.cancel
	done := s.opDone
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	s.mu.Lock()
	controller := s.controller
	app := s.app
	appClosed := s.appClosed
	s.mu.Unlock()
	var stopErr error
	if controller != nil {
		stopErr = controller.Stop()
	}
	if app != nil && !appClosed {
		_ = app.Close()
		s.mu.Lock()
		s.appClosed = true
		s.mu.Unlock()
	}
	if stopErr != nil {
		return ErrCloseStopFailed
	}
	s.mu.Lock()
	s.closeComplete = true
	s.mu.Unlock()
	return nil
}

func (s *Service) dispatch(method string, params json.RawMessage) (Snapshot, *rpcError) {
	switch method {
	case "rules.read", "rules.preview", "rules.apply":
		return s.fileRuleOperation(method, params)
	case "settings.pickBackup":
		var p struct{}
		if !decodeParams(params, &p) {
			return s.snapshot(), errorFor("invalid_params")
		}
		return s.pickBackup()
	case "connections.save", "connections.select", "connections.remove", "settings.backup", "settings.restore", "developer.configure", "developer.saveProfile", "developer.removeProfile":
		return s.desktopMutation(method, params)
	case "snapshot":
		var p struct{}
		if !decodeParams(params, &p) {
			return s.snapshot(), errorFor("invalid_params")
		}
		return s.snapshot(), nil
	case "connection.connect":
		var p struct{}
		if !decodeParams(params, &p) {
			return s.snapshot(), errorFor("invalid_params")
		}
		return s.startConnectionOperation(method, false)
	case "connection.reconnect":
		var p struct{}
		if !decodeParams(params, &p) {
			return s.snapshot(), errorFor("invalid_params")
		}
		return s.startConnectionOperation(method, true)
	case "connection.cancel":
		var p struct{}
		if !decodeParams(params, &p) {
			return s.snapshot(), errorFor("invalid_params")
		}
		if err := s.cancelOperation(); err != nil {
			return s.snapshot(), errorFor(codeFor(err))
		}
		return s.snapshot(), nil
	case "connection.disconnect":
		var p struct{}
		if !decodeParams(params, &p) {
			return s.snapshot(), errorFor("invalid_params")
		}
		return s.startDisconnect()
	case "connection.setTransport":
		var p struct {
			Transport string `json:"transport"`
		}
		if !decodeParams(params, &p) || !allowedTransport(config.ConnectionTransport(p.Transport)) {
			return s.snapshot(), errorFor("invalid_transport")
		}
		return s.startTransportChange(config.ConnectionTransport(p.Transport))
	case "workspace.pick":
		var p struct{}
		if !decodeParams(params, &p) {
			return s.snapshot(), errorFor("invalid_params")
		}
		picked, err := s.pickWorkspace()
		if err != nil {
			return s.snapshot(), errorFor(codeFor(err))
		}
		return picked, nil
	case "workspace.add":
		var p struct {
			ExpectedRevision string   `json:"expected_revision"`
			Paths            []string `json:"paths"`
			DisplayName      string   `json:"display_name"`
		}
		if !decodeParams(params, &p) || p.ExpectedRevision == "" {
			return s.snapshot(), errorFor("invalid_params")
		}
		return s.startWorkspaceMutation(method, p.ExpectedRevision, func(ctx context.Context) (workspaceadmin.Change, error) {
			return s.workspaces.AddWithDisplayName(ctx, s.targetID(), p.ExpectedRevision, p.Paths, p.DisplayName)
		}, func(current workspaceadmin.Workspace) bool {
			for _, path := range p.Paths {
				foundEnabled := false
				clean := filepath.Clean(path)
				for _, root := range current.Roots {
					if sameLocalPath(clean, filepath.Clean(root.Path)) {
						if root.Enabled {
							foundEnabled = true
						}
						break
					}
				}
				if !foundEnabled {
					return true
				}
			}
			return false
		})
	case "workspace.update":
		var p struct {
			ExpectedRevision string  `json:"expected_revision"`
			RootID           string  `json:"root_id"`
			DisplayName      *string `json:"display_name"`
			Enabled          *bool   `json:"enabled"`
		}
		if !decodeParams(params, &p) || p.ExpectedRevision == "" || p.RootID == "" || p.DisplayName == nil && p.Enabled == nil {
			return s.snapshot(), errorFor("invalid_params")
		}
		return s.startWorkspaceMutation(method, p.ExpectedRevision, func(ctx context.Context) (workspaceadmin.Change, error) {
			return s.workspaces.Update(ctx, s.targetID(), p.ExpectedRevision, p.RootID, workspaceadmin.RootUpdate{DisplayName: p.DisplayName, Enabled: p.Enabled})
		}, func(current workspaceadmin.Workspace) bool {
			if p.Enabled == nil {
				return false
			}
			for _, root := range current.Roots {
				if root.ID == p.RootID {
					return root.Enabled != *p.Enabled
				}
			}
			return false
		})
	case "workspace.remove":
		var p struct {
			ExpectedRevision string   `json:"expected_revision"`
			RootIDs          []string `json:"root_ids"`
		}
		if !decodeParams(params, &p) || p.ExpectedRevision == "" {
			return s.snapshot(), errorFor("invalid_params")
		}
		return s.startWorkspaceMutation(method, p.ExpectedRevision, func(ctx context.Context) (workspaceadmin.Change, error) {
			return s.workspaces.Remove(ctx, s.targetID(), p.ExpectedRevision, p.RootIDs)
		}, func(current workspaceadmin.Workspace) bool {
			selected := make(map[string]struct{}, len(p.RootIDs))
			for _, id := range p.RootIDs {
				selected[id] = struct{}{}
			}
			for _, root := range current.Roots {
				if _, ok := selected[root.ID]; ok && root.Enabled {
					return true
				}
			}
			return false
		})
	case "logs.export":
		var p struct{}
		if !decodeParams(params, &p) {
			return s.snapshot(), errorFor("invalid_params")
		}
		ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
		defer cancel()
		if err := s.exportLogs(ctx); err != nil {
			return s.snapshot(), errorFor(codeFor(err))
		}
		return s.snapshot(), nil
	case "diagnostics.export":
		var p struct{}
		if !decodeParams(params, &p) {
			return s.snapshot(), errorFor("invalid_params")
		}
		ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
		defer cancel()
		if err := s.exportDiagnostics(ctx); err != nil {
			return s.snapshot(), errorFor(codeFor(err))
		}
		return s.snapshot(), nil
	default:
		return s.snapshot(), errorFor("unknown_method")
	}
}

func (s *Service) snapshot() Snapshot {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	view, _ := s.uiModel.Refresh(ctx)
	workspace := WorkspaceProjection{Roots: []workspaceadmin.WorkspaceRoot{}}
	if view.Status == string(previewapp.ConfigConfigured) {
		local, err := s.workspaces.List(ctx, s.targetID())
		if err != nil {
			workspace.ErrorCode = workspaceErrorCode(err)
		} else {
			workspace.Available = true
			workspace.ConnectionID = local.ConnectionID
			workspace.ProfileID = local.ProfileID
			workspace.Revision = local.Revision
			if len(local.Roots) > previewui.MaxWorkspaceFolders {
				workspace.Truncated = true
				local.Roots = local.Roots[:previewui.MaxWorkspaceFolders]
			}
			usedBytes := 0
			for _, root := range local.Roots {
				if len(root.Path) > previewui.MaxWorkspacePathBytes || !utf8.ValidString(root.Path) {
					workspace.Truncated = true
					continue
				}
				if usedBytes+len(root.Path) > maxWorkspaceBytes {
					workspace.Truncated = true
					break
				}
				usedBytes += len(root.Path)
				workspace.Roots = append(workspace.Roots, root)
			}
		}
	} else if view.Unconfigured {
		workspace.ErrorCode = "config_missing"
	} else {
		workspace.ErrorCode = "config_invalid"
	}
	return Snapshot{Connection: s.connectionProjection(), Workspace: workspace, View: view, Desktop: s.desktopProjection()}
}

func (s *Service) connectionProjection() ConnectionProjection {
	s.mu.Lock()
	controller := s.controller
	generation := s.generation
	cancelRequested := s.cancelRequested
	status := s.status
	known := s.statusKnown
	s.mu.Unlock()
	if controller != nil && !cancelRequested {
		current := controller.Status()
		s.mu.Lock()
		if s.controller == controller && s.generation == generation {
			s.status = current
			status = current
			known = s.statusKnown
		} else {
			status = s.status
			known = s.statusKnown
		}
	}
	if controller == nil || cancelRequested {
		s.mu.Lock()
		if s.controller != controller || s.generation != generation {
			status = s.status
			known = s.statusKnown
		}
	}
	projection := ConnectionProjection{
		Transport:       string(s.transport),
		Stage:           string(status.Stage),
		Code:            string(status.Code),
		Busy:            s.busy,
		Generation:      s.generation,
		Operation:       s.operation,
		StatusKnown:     known,
		MCPReady:        status.MCPReady,
		TunnelReady:     status.TunnelReady,
		TokenConfigured: status.TokenConfigured,
		HealthScope:     "startup_checks_and_owned_processes",
		CheckedAt:       timePointer(status.UpdatedAt),
		ObservedAt:      time.Now().UTC(),
		Message:         safeStatusText(status.Message, "本机连接状态可用。"),
		Remedy:          safeStatusText(status.Remedy, "检查连接设置后重试。"),
		ChatGPTStatus:   "unverified",
		Saved:           copyBool(s.saved),
		Applied:         copyBool(s.applied),
	}
	if s.operationCode != "" {
		projection.Code = s.operationCode
		if projection.Code != string(status.Code) {
			projection.Message, projection.Remedy = messageFor(projection.Code)
		}
	}
	s.mu.Unlock()
	return projection
}

func (s *Service) pickWorkspace() (Snapshot, error) {
	s.mu.Lock()
	picker := s.options.PickFolders
	closed := s.closed
	s.mu.Unlock()
	if picker == nil || closed {
		if closed {
			return s.snapshot(), ErrClosed
		}
		return s.snapshot(), ErrPickerUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	paths, err := picker(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return s.snapshot(), err
		}
		return s.snapshot(), ErrPickerFailed
	}
	if len(paths) > workspaceadminMaxPaths {
		return s.snapshot(), ErrPickerSelectionTooLarge
	}
	selected := make([]string, 0, len(paths))
	selectedBytes := 0
	for _, path := range paths {
		if previewui.ValidateWorkspacePath(path) != nil || !filepath.IsAbs(path) {
			continue
		}
		if len(path) > previewui.MaxWorkspacePathBytes || selectedBytes+len(path) > maxWorkspaceBytes {
			return s.snapshot(), ErrPickerSelectionTooLarge
		}
		selectedBytes += len(path)
		selected = append(selected, path)
	}
	result := s.snapshot()
	result.Paths = selected
	return result, nil
}

func (s *Service) startConnectionOperation(name string, reconnect bool) (Snapshot, *rpcError) {
	err := s.startAsync(name, func(ctx context.Context, generation uint64) operationResult {
		if snapshot, err := s.store.Load(); err == nil {
			if connection, ok := snapshot.Config().Connection(s.targetID()); ok && !connection.Enabled() {
				return operationResult{code: "connection_disabled", applied: boolPtr(false)}
			}
		}
		if workspace, err := s.workspaces.List(ctx, s.targetID()); err == nil && !hasEnabledWorkspaceRoot(workspace.Roots) {
			status := previewconnect.Status{Stage: previewconnect.StageIdle, Transport: s.transport, Code: previewconnect.Code("workspace_scope_empty"), Message: "没有启用的授权目录。", Remedy: "先添加或恢复至少一个目录。"}
			return operationResult{status: &status, code: "workspace_scope_empty", applied: boolPtr(false)}
		}
		controller, err := s.ensureController(generation)
		if err != nil {
			return operationResult{status: failedStatus(s.transport, previewconnect.CodeInternal), code: "controller_unavailable", applied: boolPtr(false)}
		}
		progress := func(status previewconnect.Status) { s.updateProgress(generation, status) }
		var status previewconnect.Status
		if reconnect {
			status, _ = controller.Reconnect(ctx, progress)
		} else {
			status, _ = controller.Connect(ctx, progress)
		}
		return operationResult{status: &status, code: string(status.Code), applied: boolPtr(status.Stage == previewconnect.StageReady && status.Code == previewconnect.CodeNone)}
	})
	if err != nil {
		return s.snapshot(), errorFor(codeFor(err))
	}
	return s.snapshot(), nil
}

func (s *Service) startDisconnect() (Snapshot, *rpcError) {
	err := s.startAsync("connection.disconnect", func(_ context.Context, generation uint64) operationResult {
		s.mu.Lock()
		controller := s.controller
		s.mu.Unlock()
		if controller == nil {
			status := previewconnect.Status{Stage: previewconnect.StageIdle, Transport: s.transport, Message: "未连接。", Remedy: "点击连接以执行本机启动检查。"}
			return operationResult{status: &status, applied: boolPtr(true)}
		}
		err := controller.Stop()
		status := controller.Status()
		if err != nil {
			return operationResult{status: &status, code: "stop_failed", applied: boolPtr(false)}
		}
		return operationResult{status: &status, applied: boolPtr(true)}
	})
	if err != nil {
		return s.snapshot(), errorFor(codeFor(err))
	}
	return s.snapshot(), nil
}

func (s *Service) startTransportChange(transport config.ConnectionTransport) (Snapshot, *rpcError) {
	s.mu.Lock()
	if s.transport == transport && !s.busy {
		s.mu.Unlock()
		return s.snapshot(), nil
	}
	s.mu.Unlock()
	err := s.startAsync("connection.setTransport", func(ctx context.Context, generation uint64) operationResult {
		s.mu.Lock()
		old := s.controller
		previous := s.transport
		s.mu.Unlock()
		if old != nil {
			if err := old.Stop(); err != nil {
				status := old.Status()
				return operationResult{status: &status, code: "stop_failed", applied: boolPtr(false)}
			}
			s.mu.Lock()
			if s.controller == old {
				s.controller = nil
			}
			s.mu.Unlock()
		}
		if err := ctx.Err(); err != nil {
			status := previewconnect.Status{Stage: previewconnect.StageCancelled, Code: previewconnect.CodeCancelled, Transport: previous}
			return operationResult{status: &status, code: "cancelled", applied: boolPtr(false)}
		}
		next, err := s.controllerFactory(transport)
		if err != nil {
			status := failedStatus(previous, previewconnect.CodeInternal)
			return operationResult{status: status, code: "transport_unavailable", applied: boolPtr(false)}
		}
		s.mu.Lock()
		if s.generation != generation || s.closed {
			s.mu.Unlock()
			_ = next.Stop()
			status := previewconnect.Status{Stage: previewconnect.StageCancelled, Code: previewconnect.CodeCancelled, Transport: previous}
			return operationResult{status: &status, code: "cancelled", applied: boolPtr(false)}
		}
		s.controller = next
		s.transport = transport
		s.status = next.Status()
		s.statusKnown = false
		s.mu.Unlock()
		status := next.Status()
		return operationResult{status: &status, applied: boolPtr(true)}
	})
	if err != nil {
		return s.snapshot(), errorFor(codeFor(err))
	}
	return s.snapshot(), nil
}

func (s *Service) startWorkspaceMutation(name, expectedRevision string, mutate func(context.Context) (workspaceadmin.Change, error), scopeChange func(workspaceadmin.Workspace) bool) (Snapshot, *rpcError) {
	err := s.startAsync(name, func(ctx context.Context, generation uint64) (result operationResult) {
		current, err := s.workspaces.List(ctx, s.targetID())
		if err != nil {
			return operationResult{code: workspaceErrorCode(err), saved: boolPtr(false), applied: boolPtr(false)}
		}
		if current.Revision != expectedRevision {
			return operationResult{code: string(workspaceadmin.CodeRevisionConflict), saved: boolPtr(false), applied: boolPtr(false)}
		}
		permissionChange := scopeChange != nil && scopeChange(current)
		if permissionChange {
			ownershipRevision, references, err := s.workspaces.ProfileReferenceCount(ctx, s.targetID())
			if err != nil {
				return operationResult{code: workspaceErrorCode(err), saved: boolPtr(false), applied: boolPtr(false)}
			}
			if ownershipRevision != expectedRevision {
				return operationResult{code: string(workspaceadmin.CodeRevisionConflict), saved: boolPtr(false), applied: boolPtr(false)}
			}
			if references > 1 {
				return operationResult{code: "profile_shared", saved: boolPtr(false), applied: boolPtr(false)}
			}
		}
		s.mu.Lock()
		controller := s.controller
		s.mu.Unlock()
		wasRunning := false
		var previousStatus previewconnect.Status
		stopped := false
		if controller != nil {
			previousStatus = controller.Status()
			wasRunning = previousStatus.Stage == previewconnect.StageReady && previousStatus.MCPReady && previousStatus.TunnelReady
			if permissionChange {
				if err := controller.Stop(); err != nil {
					status := controller.Status()
					return operationResult{status: &status, code: "stop_failed", saved: boolPtr(false), applied: boolPtr(false)}
				}
				stopped = true
			}
		}
		change, err := mutate(ctx)
		if err != nil {
			status := previousStatus
			if controller != nil && !stopped {
				status = controller.Status()
			}
			applied := false
			if stopped && wasRunning && !s.isClosed() {
				status, _ = s.restorePreviousConnection(controller, generation)
				applied = status.Stage == previewconnect.StageReady && status.Code == previewconnect.CodeNone
			}
			return operationResult{status: &status, code: workspaceErrorCode(err), saved: boolPtr(false), applied: boolPtr(applied)}
		}
		defer func() {
			for _, root := range change.Changed {
				result.rootIDs = append(result.rootIDs, root.ID)
			}
		}()
		saved := true
		applied := true
		var status *previewconnect.Status
		if !hasEnabledWorkspaceRoot(change.Workspace.Roots) {
			emptyScopeStatus := previewconnect.Status{
				Stage:     previewconnect.StageIdle,
				Transport: s.transport,
				Code:      previewconnect.Code("workspace_scope_empty"),
				Message:   "所有工作空间均已暂停；本机连接保持停止。",
				Remedy:    "恢复至少一个已授权目录，然后显式连接。",
				UpdatedAt: time.Now().UTC(),
			}
			return operationResult{status: &emptyScopeStatus, code: string(emptyScopeStatus.Code), saved: boolPtr(saved), applied: boolPtr(false)}
		}
		if stopped && wasRunning {
			currentStatus, reconnectErr := s.reconnectController(ctx, controller, generation)
			status = &currentStatus
			applied = reconnectErr == nil && currentStatus.Stage == previewconnect.StageReady && currentStatus.Code == previewconnect.CodeNone
			if !applied {
				return operationResult{status: status, code: "reconnect_not_ready", saved: boolPtr(saved), applied: boolPtr(false)}
			}
		} else if controller != nil && stopped {
			currentStatus := controller.Status()
			status = &currentStatus
		}
		return operationResult{status: status, saved: boolPtr(saved), applied: boolPtr(applied)}
	})
	if err != nil {
		return s.snapshot(), errorFor(codeFor(err))
	}
	return s.snapshot(), nil
}

func hasEnabledWorkspaceRoot(roots []workspaceadmin.WorkspaceRoot) bool {
	for _, root := range roots {
		if root.Enabled {
			return true
		}
	}
	return false
}

func (s *Service) restorePreviousConnection(controller lifecycleController, generation uint64) (previewconnect.Status, error) {
	if controller == nil {
		return previewconnect.Status{}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	return controller.Reconnect(ctx, func(status previewconnect.Status) { s.updateProgress(generation, status) })
}

func (s *Service) reconnectController(ctx context.Context, controller lifecycleController, generation uint64) (previewconnect.Status, error) {
	if controller == nil {
		return previewconnect.Status{}, errors.New("controller unavailable")
	}
	return controller.Reconnect(ctx, func(status previewconnect.Status) { s.updateProgress(generation, status) })
}

func (s *Service) exportLogs(ctx context.Context) error {
	s.mu.Lock()
	exporter := s.options.Export
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return ErrClosed
	}
	if exporter == nil {
		return ErrExportUnavailable
	}
	view, err := s.uiModel.Refresh(ctx)
	if err != nil {
		return ErrExportFailed
	}
	type logDocument struct {
		ManagementEvents []ManagementEvent    `json:"management_events"`
		SchemaVersion    string               `json:"schema_version"`
		GeneratedAt      time.Time            `json:"generated_at"`
		Logs             []previewui.LogEntry `json:"logs"`
		Truncated        bool                 `json:"truncated"`
		Omitted          []string             `json:"omitted"`
	}
	document := logDocument{
		ManagementEvents: s.desktopProjection().Events,
		SchemaVersion:    previewui.SchemaVersion,
		GeneratedAt:      time.Now().UTC(),
		Logs:             append([]previewui.LogEntry(nil), view.Logs...),
		Truncated:        view.Audit.Truncated,
		Omitted:          []string{"paths", "raw_audit_lines", "credentials", "process_details", "network_endpoints"},
	}
	data, err := json.Marshal(document)
	if err != nil {
		return ErrExportFailed
	}
	clean, err := previewui.ValidateAndScrubExport(data)
	if err != nil {
		return ErrExportRejected
	}
	if len(clean) > previewui.MaxExportBytes {
		return ErrExportTooLarge
	}
	if err := exporter(ctx, ExportLogs, append([]byte(nil), clean...)); err != nil {
		return ErrExportFailed
	}
	return nil
}

func (s *Service) exportDiagnostics(ctx context.Context) error {
	s.mu.Lock()
	exporter := s.options.Export
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return ErrClosed
	}
	if exporter == nil {
		return ErrExportUnavailable
	}
	data, err := s.uiModel.ExportDiagnostics(ctx)
	if err != nil {
		return ErrExportFailed
	}
	clean, err := previewui.ValidateAndScrubExport(data)
	if err != nil || len(clean) > previewui.MaxExportBytes {
		return ErrExportRejected
	}
	if err := exporter(ctx, ExportDiagnostics, append([]byte(nil), clean...)); err != nil {
		return ErrExportFailed
	}
	return nil
}

func (s *Service) startAsync(name string, run func(context.Context, uint64) operationResult) error {
	s.mu.Lock()
	if s.closed || s.busy || s.nativeBusy {
		s.mu.Unlock()
		if s.closed {
			return ErrClosed
		}
		return ErrBusy
	}
	s.generation++
	generation := s.generation
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	s.busy = true
	s.cancelRequested = false
	s.cancel = cancel
	s.operation = name
	s.operationCode = ""
	s.saved, s.applied = nil, nil
	done := make(chan struct{})
	s.opDone = done
	s.mu.Unlock()
	go func() {
		result := operationResult{}
		func() {
			defer func() {
				if recover() != nil {
					result = operationResult{code: "internal_error", saved: boolPtr(false), applied: boolPtr(false)}
				}
			}()
			result = run(ctx, generation)
		}()
		s.mu.Lock()
		cancelled := s.cancelRequested || ctx.Err() != nil
		controller := s.controller
		s.mu.Unlock()
		if cancelled && (name == "connection.connect" || name == "connection.reconnect") && controller != nil {
			stopErr := controller.Stop()
			status := controller.Status()
			result.status = &status
			if stopErr != nil {
				result.code = "stop_failed"
				result.applied = boolPtr(false)
			} else {
				result.code = "cancelled"
				result.applied = boolPtr(false)
			}
		}
		cancel()
		s.mu.Lock()
		if s.opDone == done {
			if s.generation == generation {
				if result.status != nil {
					s.status = *result.status
					s.statusKnown = true
				}
				s.operationCode = result.code
				s.saved = copyBool(result.saved)
				s.applied = copyBool(result.applied)
			} else if s.cancelRequested {
				// Cancellation increments generation so late progress/results cannot
				// replace the current view. Persisted facts are retained if a config
				// write completed in the cancellation race.
				if result.saved != nil {
					s.saved = copyBool(result.saved)
				}
				if result.applied != nil {
					s.applied = copyBool(result.applied)
				}
				if result.status != nil && (result.status.Stage == previewconnect.StageIdle || result.status.Stage == previewconnect.StageCancelled) {
					s.status = *result.status
					s.statusKnown = true
				}
				if result.code == "stop_failed" {
					s.operationCode = result.code
					if result.status != nil {
						s.status = *result.status
						s.statusKnown = true
					}
				} else if result.saved != nil && *result.saved && result.applied != nil && !*result.applied {
					s.operationCode = "reconnect_not_ready"
				}
			}
			s.busy = false
			s.cancel = nil
			s.cancelRequested = false
			s.opDone = nil
		}
		s.recordEventLocked(name, result.code, result.rootIDs)
		close(done)
		s.mu.Unlock()
	}()
	return nil
}

func (s *Service) ensureController(generation uint64) (lifecycleController, error) {
	s.mu.Lock()
	if s.closed || s.generation != generation {
		s.mu.Unlock()
		return nil, ErrClosed
	}
	if s.controller != nil {
		controller := s.controller
		s.mu.Unlock()
		return controller, nil
	}
	transport := s.transport
	factory := s.controllerFactory
	s.mu.Unlock()
	controller, err := factory(transport)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.generation != generation {
		_ = controller.Stop()
		return nil, ErrClosed
	}
	if s.controller == nil {
		s.controller = controller
	}
	return s.controller, nil
}

func (s *Service) newController(transport config.ConnectionTransport) (lifecycleController, error) {
	if !allowedTransport(transport) {
		return nil, ErrInvalidOptions
	}
	options := previewconnect.DefaultOptions(s.options.RepoRoot)
	options.Transport = transport
	// The MCP child and workspaceadmin must use the same explicitly selected
	// config file; transport defaults never replace this trusted host option.
	options.MCPConfig = s.options.ConfigPath
	options.ConnectionID = s.targetID()
	if snapshot, loadErr := s.store.Load(); loadErr == nil {
		if connection, ok := snapshot.Config().Connection(options.ConnectionID); ok && connection.DesktopPort() != 0 {
			options.MCPListenAddr = "127.0.0.1:" + strconv.Itoa(connection.DesktopPort())
		}
	}
	controller, err := previewconnect.New(options)
	if err != nil {
		return nil, ErrInvalidOptions
	}
	return controller, nil
}

func (s *Service) updateProgress(generation uint64, status previewconnect.Status) {
	s.mu.Lock()
	if s.generation == generation && s.busy && !s.cancelRequested && !s.closed {
		s.status = status
		s.statusKnown = true
	}
	s.mu.Unlock()
}

func (s *Service) cancelOperation() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	if !s.busy || s.cancel == nil {
		s.mu.Unlock()
		return nil
	}
	s.cancelRequested = true
	s.generation++
	cancel := s.cancel
	s.status.Stage = previewconnect.StageStopping
	s.status.MCPReady = false
	s.status.TunnelReady = false
	s.status.Code = previewconnect.CodeCancelled
	s.status.Message = "正在取消本次本机操作。"
	s.status.Remedy = "等待当前操作完成后再发起新的请求。"
	s.operationCode = "cancelled"
	s.mu.Unlock()
	cancel()
	return nil
}

func (s *Service) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func allowedTransport(transport config.ConnectionTransport) bool {
	return transport == config.TransportOpenAIRuntime || transport == config.TransportCloudflareNamed
}

func failedStatus(transport config.ConnectionTransport, code previewconnect.Code) *previewconnect.Status {
	return &previewconnect.Status{Stage: previewconnect.StageFailed, Transport: transport, Code: code, Message: "连接控制器不可用。", Remedy: "检查本地 Preview 配置后重试。"}
}

func validAbsolutePath(value string) bool {
	return value != "" && filepath.IsAbs(value) && !strings.ContainsRune(value, 0)
}

func decodeParams(raw json.RawMessage, target any) bool {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if !jsonObject(raw) {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return false
	}
	return ensureEOF(decoder) == nil
}

func jsonObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) >= 2 && trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}'
}

func validRPCID(raw json.RawMessage) bool {
	if len(raw) > 128 {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil || ensureEOF(decoder) != nil {
		return false
	}
	switch value.(type) {
	case nil, string, json.Number:
		return true
	default:
		return false
	}
}

func ensureEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func encodeResponse(response rpcResponse) string {
	data, err := json.Marshal(response)
	if err != nil || len(data) > maxResponseBytes {
		return `{"id":null,"ok":false,"error":{"code":"response_too_large","message":"Response exceeds the safe limit."}}`
	}
	return string(data)
}

func errorFor(code string) *rpcError {
	message, _ := messageFor(code)
	return &rpcError{Code: code, Message: message}
}

func messageFor(code string) (string, string) {
	switch code {
	case "invalid_request", "invalid_params":
		return "请求格式无效。", "检查请求字段后重试。"
	case "unknown_method":
		return "不支持此操作。", "使用当前 Preview 提供的固定操作。"
	case "invalid_transport":
		return "传输类型无效。", "仅支持 OpenAI Runtime 或 Cloudflare Named。"
	case "busy":
		return "已有本机操作正在运行。", "等待当前操作完成后重试。"
	case "closed":
		return "本地 Preview 已关闭。", "重新打开 Preview 后重试。"
	case "picker_unavailable":
		return "本机文件夹选择器不可用。", "重新打开 Preview 后重试。"
	case "picker_failed":
		return "本机文件夹选择未完成。", "重新打开文件夹选择器后重试。"
	case "picker_selection_too_large":
		return "所选文件夹数量或路径总长度超出本地限制。", "减少文件夹数量后重试。"
	case "export_unavailable", "export_failed", "export_rejected", "export_too_large":
		return "本地导出失败。", "检查本机保存位置后重试。"
	case "operation_timeout":
		return "本机操作超时。", "检查本机运行状态后重试。"
	case "config_missing":
		return "尚未初始化 Local-Probe 配置。", "运行随包提供的初始化脚本后刷新。"
	case "config_invalid":
		return "Local-Probe 配置无效。", "运行初始化脚本或修复配置后刷新。"
	case "profile_shared":
		return "此 profile 被多个 connection 共用，未修改授权范围。", "先为目标 connection 配置独立 profile，再调整工作空间授权。"
	case "revision_conflict":
		return "配置已被其他操作修改。", "刷新工作空间列表后重试。"
	case "stop_failed":
		return "本机拥有的旧连接未能安全停止。", "确认本次 Preview 启动的进程已退出后重试。"
	case "reconnect_not_ready":
		return "配置已保存，但新连接未就绪。", "检查连接状态后重试；已保存的配置不会自动回滚。"
	case "workspace_scope_empty":
		return "没有启用的授权工作空间；本机连接保持停止。", "恢复至少一个已授权目录，然后显式连接。"
	case "backup_invalid":
		return "所选备份的格式、版本、内容或完整性校验未通过。", "选择此版本生成的 .lpbackup 文件；当前配置未更改。"
	case "backup_unavailable", "backup_read_failed":
		return "备份文件选择或读取未完成。", "检查所选备份文件及本机权限。"
	case "backup_failed":
		return "备份未保存。", "选择可写目录，使用尚不存在的名称；后缀由程序添加。"
	case "backup_selection_expired":
		return "所选备份已过期或不匹配当前配置版本。", "重新选择文件并确认恢复。"
	case "controller_unavailable", "transport_unavailable":
		return "本机连接控制器不可用。", "检查本机 Preview 运行环境后重试。"
	case "cancelled":
		return "本机操作已取消。", "等待清理完成后再重试。"
	case "internal_error":
		return "本机操作失败。", "刷新状态后重试。"
	default:
		return "本机操作未完成。", "刷新状态后重试。"
	}
}

func codeFor(err error) string {
	if errors.Is(err, ErrBusy) {
		return "busy"
	}
	if errors.Is(err, ErrClosed) {
		return "closed"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "operation_timeout"
	}
	if errors.Is(err, ErrPickerUnavailable) {
		return "picker_unavailable"
	}
	if errors.Is(err, ErrPickerFailed) {
		return "picker_failed"
	}
	if errors.Is(err, ErrPickerSelectionTooLarge) {
		return "picker_selection_too_large"
	}
	if errors.Is(err, ErrExportUnavailable) {
		return "export_unavailable"
	}
	if errors.Is(err, ErrExportRejected) {
		return "export_rejected"
	}
	if errors.Is(err, ErrExportTooLarge) {
		return "export_too_large"
	}
	if errors.Is(err, ErrExportFailed) {
		return "export_failed"
	}
	return "export_failed"
}

func workspaceErrorCode(err error) string {
	if err == nil {
		return ""
	}
	if code := workspaceadmin.ProblemCode(err); code != "" {
		return string(code)
	}
	if errors.Is(err, config.ErrConfigNotFound) {
		return "config_missing"
	}
	if errors.Is(err, config.ErrRevisionConflict) {
		return string(workspaceadmin.CodeRevisionConflict)
	}
	if errors.Is(err, config.ErrInvalid) {
		return "config_invalid"
	}
	return "workspace_error"
}

func safeStatusText(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 512 || !utf8.ValidString(value) || strings.ContainsAny(value, `/\\`) {
		return fallback
	}
	lower := strings.ToLower(value)
	for _, marker := range []string{"bearer", "token", "secret", "password", "authorization", "http:", "key="} {
		if strings.Contains(lower, marker) {
			return fallback
		}
	}
	return value
}

func copyBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copyOfValue := *value
	return &copyOfValue
}

func boolPtr(value bool) *bool { return &value }

func timePointer(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

func sameLocalPath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

const workspaceadminMaxPaths = 128
