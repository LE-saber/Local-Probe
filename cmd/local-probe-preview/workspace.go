package main

import (
	"context"
	"errors"

	"github.com/LE-saber/Local-Probe/internal/config"
	"github.com/LE-saber/Local-Probe/internal/previewconnect"
	"github.com/LE-saber/Local-Probe/internal/previewui"
	"github.com/LE-saber/Local-Probe/internal/workspaceadmin"
)

const previewWorkspaceConnection = workspaceadmin.DefaultConnectionID

// workspaceReconnecter keeps workspaceadmin independent from the concrete
// tunnel controller while making the post-save reconnect requirement explicit.
type workspaceReconnecter interface {
	Reconnect(context.Context, previewconnect.ProgressFunc) (previewconnect.Status, error)
}

// previewWorkspaceManager adapts the revision-aware configuration manager to
// the native Preview UI. Each mutation reads the current revision, applies a
// CAS update for chatgpt-local only, then reconnects the owned local MCP/Tunnel
// so the new authorization is active for subsequent GPT requests.
type previewWorkspaceManager struct {
	admin      *workspaceadmin.Manager
	controller workspaceReconnecter
}

var _ previewui.WorkspaceManager = (*previewWorkspaceManager)(nil)

func newPreviewWorkspaceManager(admin *workspaceadmin.Manager, controller workspaceReconnecter) *previewWorkspaceManager {
	return &previewWorkspaceManager{admin: admin, controller: controller}
}

func (m *previewWorkspaceManager) List(ctx context.Context) ([]previewui.WorkspaceFolder, error) {
	if m == nil || m.admin == nil {
		return nil, previewui.ErrWorkspaceManagerUnavailable
	}
	workspace, err := m.admin.List(ctx, previewWorkspaceConnection)
	if err != nil {
		return nil, workspaceErrorPresentation(err)
	}
	folders := make([]previewui.WorkspaceFolder, 0, len(workspace.Roots))
	for _, root := range workspace.Roots {
		folders = append(folders, previewui.WorkspaceFolder{RootID: root.ID, Path: root.Path})
	}
	return folders, nil
}

func (m *previewWorkspaceManager) Add(ctx context.Context, paths []string) error {
	if m == nil || m.admin == nil || m.controller == nil {
		return previewui.ErrWorkspaceManagerUnavailable
	}
	workspace, err := m.admin.List(ctx, previewWorkspaceConnection)
	if err != nil {
		return workspaceErrorPresentation(err)
	}
	if _, err := m.admin.Add(ctx, previewWorkspaceConnection, workspace.Revision, paths); err != nil {
		return workspaceErrorPresentation(err)
	}
	return workspaceErrorPresentation(m.reconnectAfterSave(ctx))
}

func (m *previewWorkspaceManager) Remove(ctx context.Context, rootIDs []string) error {
	if m == nil || m.admin == nil || m.controller == nil {
		return previewui.ErrWorkspaceManagerUnavailable
	}
	workspace, err := m.admin.List(ctx, previewWorkspaceConnection)
	if err != nil {
		return workspaceErrorPresentation(err)
	}
	if _, err := m.admin.Remove(ctx, previewWorkspaceConnection, workspace.Revision, rootIDs); err != nil {
		return workspaceErrorPresentation(err)
	}
	return workspaceErrorPresentation(m.reconnectAfterSave(ctx))
}

func (m *previewWorkspaceManager) reconnectAfterSave(ctx context.Context) error {
	status, err := m.controller.Reconnect(ctx, nil)
	if err == nil && status.Code == previewconnect.CodeNone && status.Stage == previewconnect.StageReady {
		return nil
	}
	// The config mutation has already succeeded. Return only a stable code and
	// safe remediation; never forward controller paths, child output, or token
	// material to the UI.
	code := status.Code
	if code == previewconnect.CodeNone {
		code = previewconnect.CodeInternal
	}
	return &workspaceReconnectError{code: code, cause: safeReconnectCause(err)}
}

type workspaceReconnectError struct {
	code  previewconnect.Code
	cause error
}

func (e *workspaceReconnectError) Error() string {
	if e == nil {
		return "授权已保存，但连接尚未就绪；请到连接页查看原因并重试。"
	}
	return "授权已保存，但连接尚未就绪；请到连接页查看原因并重试。"
}

func (e *workspaceReconnectError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func safeReconnectCause(err error) error {
	if err == nil {
		return nil
	}
	var problem *previewconnect.Problem
	if errors.As(err, &problem) && problem != nil {
		return &previewconnect.Problem{Code: problem.Code, Message: problem.Message, Remedy: problem.Remedy}
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return errors.New("workspace reconnect failed")
}

// safeWorkspaceError is the adapter's presentation boundary. Its text is
// safe to show directly in the GUI and deliberately contains no selected
// path, config filename, token, child output, or OS error detail. The cause
// remains available to local tests/diagnostics through errors.Is/As only.
type safeWorkspaceError struct {
	code        string
	message     string
	remediation string
	cause       error
}

var _ previewui.SafeWorkspaceError = (*safeWorkspaceError)(nil)

func (e *safeWorkspaceError) Error() string {
	if e == nil || e.message == "" {
		return "工作空间操作失败，请重试。"
	}
	return e.message
}

func (e *safeWorkspaceError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// WorkspaceUserMessage and WorkspaceRemediation are the safe error seam used
// by the native UI. Both values are fixed local text; no path, token, child
// output, or raw OS error crosses this boundary.
func (e *safeWorkspaceError) WorkspaceUserMessage() string { return e.Error() }
func (e *safeWorkspaceError) WorkspaceRemediation() string {
	if e == nil || e.remediation == "" {
		return "请重试工作空间操作。"
	}
	return e.remediation
}

// WorkspaceMessage and WorkspaceCode are retained as narrow compatibility
// helpers for older local callers; new UI code should use SafeWorkspaceError.
func (e *safeWorkspaceError) WorkspaceMessage() string { return e.Error() }
func (e *safeWorkspaceError) WorkspaceCode() string {
	if e == nil {
		return "workspace_error"
	}
	return e.code
}

func workspaceErrorPresentation(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := err.(*safeWorkspaceError); ok {
		return err
	}
	var reconnect *workspaceReconnectError
	if errors.As(err, &reconnect) {
		return &safeWorkspaceError{
			code:    "reconnect_not_ready",
			message: reconnect.Error(),
			cause:   reconnect,
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	code := workspaceadmin.ProblemCode(err)
	messages := map[workspaceadmin.Code][2]string{
		workspaceadmin.CodePathMissing:        {"path_missing", "所选文件夹不存在，请重新选择一个已存在的文件夹。"},
		workspaceadmin.CodeNotDirectory:       {"not_directory", "所选路径不是文件夹，请重新选择文件夹。"},
		workspaceadmin.CodePathLink:           {"symlink_or_reparse", "符号链接、junction 或 reparse 文件夹不能授权，请选择实际文件夹。"},
		workspaceadmin.CodeRemotePath:         {"remote_path", "网络共享或映射网络盘不能授权，请选择本机固定磁盘文件夹。"},
		workspaceadmin.CodeNonFixedDrive:      {"non_fixed_drive", "工作空间必须位于本机固定磁盘，请重新选择。"},
		workspaceadmin.CodeDriveRoot:          {"drive_root", "不能授权整个磁盘根目录，请选择具体项目文件夹。"},
		workspaceadmin.CodeRootOverlap:        {"root_overlap", "新增文件夹与已有工作空间存在父子重叠，请只保留一个授权范围。"},
		workspaceadmin.CodeRevisionConflict:   {"revision_conflict", "配置已被其他窗口修改，请刷新工作空间列表后重试。"},
		workspaceadmin.CodeMutationBusy:       {"busy", "已有窗口正在修改工作空间，请稍后重试。"},
		workspaceadmin.CodeRootMissing:        {"root_not_authorized", "只能删除当前连接已授权的文件夹，请刷新后重试。"},
		workspaceadmin.CodeConnectionMissing:  {"connection_missing", "找不到 chatgpt-local 连接，请先完成连接配置。"},
		workspaceadmin.CodeProfileMissing:     {"profile_missing", "连接对应的访问配置不存在，请修复配置后重试。"},
		workspaceadmin.CodeConfigInvalid:      {"config_invalid", "工作空间配置无效，请修复配置后重试。"},
		workspaceadmin.CodePersistenceFailure: {"persistence_failure", "工作空间保存失败，请检查配置文件权限和磁盘状态。"},
		workspaceadmin.CodeInvalidPath:        {"invalid_path", "文件夹路径无效，请重新选择本地文件夹。"},
		workspaceadmin.CodeInvalidSelection:   {"invalid_selection", "选择的文件夹项目无效，请刷新后重试。"},
		workspaceadmin.CodeNoPaths:            {"no_paths", "请至少选择一个文件夹。"},
		workspaceadmin.CodeTooManyPaths:       {"too_many_paths", "一次选择的文件夹过多，请分批添加。"},
	}
	if entry, ok := messages[code]; ok {
		return &safeWorkspaceError{code: string(entry[0]), message: entry[1], cause: err}
	}
	return &safeWorkspaceError{code: "workspace_error", message: "工作空间操作失败，请检查文件夹权限后重试。", cause: err}
}

// newWorkspaceAdmin opens exactly the config path already selected by the
// Preview application. It does not discover another config or execute a
// command.
func newWorkspaceAdmin(configPath string) (*workspaceadmin.Manager, error) {
	store, err := config.NewFileStore(configPath)
	if err != nil {
		return nil, err
	}
	return workspaceadmin.New(store)
}
