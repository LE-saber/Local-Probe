package previewui

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"
)

const (
	// MaxWorkspaceFolders keeps a malformed or unexpectedly large manager
	// response from turning the native page into an unbounded view.
	MaxWorkspaceFolders = 128
	// MaxWorkspacePathBytes is a validation/display boundary, not a Windows
	// filesystem capability claim. The manager remains responsible for final
	// root/reparse/identity checks before granting access.
	MaxWorkspacePathBytes    = 32 << 10
	MaxWorkspaceDisplayBytes = 240
)

var (
	ErrWorkspaceManagerUnavailable = errors.New("workspace manager unavailable")
	ErrWorkspacePathEmpty          = errors.New("workspace path is empty")
	ErrWorkspacePathTooLong        = errors.New("workspace path is too long")
	ErrWorkspacePathInvalid        = errors.New("workspace path is invalid")
	ErrWorkspaceSelectionIndex     = errors.New("workspace selection index is invalid")
	ErrWorkspacePickerUnavailable  = errors.New("workspace folder picker unavailable")
	ErrWorkspacePickerSelection    = errors.New("workspace folder picker selection invalid")
)

// WorkspaceManager is the deliberately small seam used by the native Preview
// to manage local workspace access. Implementations must validate/canonicalize
// roots and persist the access list atomically; the UI never treats a path as
// an authorization decision by itself. Paths returned by List are expected to
// be absolute local paths and must not contain credentials or command text.
//
// Add and Remove accept batches so the UI can apply a multi-selection in one
// operation. A manager is optional: when it is absent, the page is explicitly
// unavailable/read-only and no folder picker is opened.
type WorkspaceManager interface {
	List(context.Context) ([]WorkspaceFolder, error)
	Add(context.Context, []string) error
	// Remove receives stable root IDs from List. A manager may accept a path
	// only for legacy entries that do not have a root ID.
	Remove(context.Context, []string) error
}

// SafeWorkspaceError is the optional error seam for a manager. The two
// strings must already be user-facing and secret-free; the Preview applies a
// second conservative scrub before displaying them. Managers that return an
// ordinary error get a generic message instead of leaking the error text.
type SafeWorkspaceError interface {
	error
	WorkspaceUserMessage() string
	WorkspaceRemediation() string
}

// WorkspaceErrorPresentation returns bounded, safe UI text for a manager
// error. It intentionally does not return the original error string.
func WorkspaceErrorPresentation(err error) (message, remediation string) {
	if err == nil {
		return "", ""
	}
	var safeErr SafeWorkspaceError
	if errors.As(err, &safeErr) {
		message = sanitizeWorkspaceNotice(safeErr.WorkspaceUserMessage())
		remediation = sanitizeWorkspaceNotice(safeErr.WorkspaceRemediation())
		if message != "" && remediation != "" {
			return message, remediation
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "工作空间操作超时", "检查文件夹权限或工作空间管理器状态后重试"
	}
	if errors.Is(err, context.Canceled) {
		return "工作空间操作已取消", "重新打开工作空间访问页后重试"
	}
	if errors.Is(err, ErrWorkspaceManagerUnavailable) {
		return "工作空间管理器不可用", "检查 Preview 启动配置并重新启动应用"
	}
	if errors.Is(err, ErrWorkspacePickerUnavailable) {
		return "无法打开文件夹选择器", "可直接输入完整文件夹路径；也可重新启动 Preview 后重试"
	}
	if errors.Is(err, ErrWorkspacePickerSelection) {
		return "所选文件夹无效", "选择现有的本地文件夹，不要选择网络位置或磁盘根目录"
	}
	if errors.Is(err, ErrWorkspacePathInvalid) || errors.Is(err, ErrWorkspacePathTooLong) {
		return "文件夹路径无效或过长", "重新选择一个有效的本地文件夹"
	}
	return "工作空间操作失败", "检查文件夹权限后重试"
}

func sanitizeWorkspaceNotice(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 240 || !utf8.ValidString(value) {
		return ""
	}
	lower := strings.ToLower(value)
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	// A safe manager message is not allowed to smuggle a path, credential,
	// command line, or URL into the status area.
	for _, marker := range []string{"\\", "/", "bearer ", "token", "password", "secret", "cmd.exe", "http:"} {
		if strings.Contains(lower, marker) {
			return ""
		}
	}
	return value
}

// WorkspaceFolder is the bounded UI projection of one authorized folder. The
// RootID is the stable logical ID advertised to MCP callers; Path is shown on
// this explicitly local management page only.
// Every folder returned by a real manager is an active GPT access root; the
// checkboxes in the page select entries for batch removal. This keeps the
// minimum manager contract (List/Add/Remove) sufficient while still allowing
// multiple entries to be selected at once.
type WorkspaceFolder struct {
	RootID string
	Path   string
}

// NormalizeWorkspaceFolders applies the presentation boundary to a manager
// response. Invalid paths are omitted, duplicate root IDs/paths are collapsed
// using a Windows-style case-insensitive key, and the result is capped.
func NormalizeWorkspaceFolders(values []WorkspaceFolder) []WorkspaceFolder {
	if len(values) == 0 {
		return nil
	}
	out := make([]WorkspaceFolder, 0, minWorkspaceInt(len(values), MaxWorkspaceFolders))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		path := strings.TrimSpace(value.Path)
		if ValidateWorkspacePath(path) != nil {
			continue
		}
		rootID := sanitizeWorkspaceRootID(value.RootID)
		key := workspaceFolderKey(WorkspaceFolder{RootID: rootID, Path: path})
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, WorkspaceFolder{RootID: rootID, Path: path})
		if len(out) >= MaxWorkspaceFolders {
			break
		}
	}
	return out
}

// ValidateWorkspacePath checks only bounded presentation input. It does not
// prove that the path exists, is a directory, is inside an authorized root, or
// is safe from reparse/identity races; those checks belong to the manager.
func ValidateWorkspacePath(path string) error {
	if path == "" {
		return ErrWorkspacePathEmpty
	}
	if len(path) > MaxWorkspacePathBytes {
		return ErrWorkspacePathTooLong
	}
	if !utf8.ValidString(path) {
		return ErrWorkspacePathInvalid
	}
	for _, r := range path {
		if r == '\r' || r == '\n' || r == '\t' || r < 0x20 || r == 0x7f {
			return ErrWorkspacePathInvalid
		}
	}
	return nil
}

// NormalizeWorkspaceInputPath applies the presentation-side normalization used
// by the native path EDIT control. It deliberately does not canonicalize or
// authorize the path: the workspace manager still performs existence,
// filesystem, reparse-point, and overlap checks before saving it.
func NormalizeWorkspaceInputPath(raw string) (string, error) {
	path := strings.TrimSpace(raw)
	if err := ValidateWorkspacePath(path); err != nil {
		return "", err
	}
	return path, nil
}

// WorkspaceDisplayPath returns a bounded, human-readable path for the local
// management page. It never changes the path passed to WorkspaceManager.
func WorkspaceDisplayPath(path string) string {
	if ValidateWorkspacePath(path) != nil {
		return "路径不可显示"
	}
	if len(path) <= MaxWorkspaceDisplayBytes {
		return path
	}
	// Keep both the root and leaf useful when a long Windows path is shown.
	const marker = "…"
	keep := MaxWorkspaceDisplayBytes - len(marker)
	if keep < 2 {
		return marker
	}
	left := keep / 2
	right := keep - left
	return workspacePrefixBytes(path, left) + marker + workspaceSuffixBytes(path, right)
}

func workspacePrefixBytes(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	var out strings.Builder
	for _, r := range value {
		width := utf8.RuneLen(r)
		if out.Len()+width > maxBytes {
			break
		}
		out.WriteRune(r)
	}
	return out.String()
}

func workspaceSuffixBytes(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	runes := []rune(value)
	var out strings.Builder
	for index := len(runes) - 1; index >= 0; index-- {
		width := utf8.RuneLen(runes[index])
		if out.Len()+width > maxBytes {
			break
		}
		out.WriteRune(runes[index])
	}
	result := []rune(out.String())
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return string(result)
}

func displayWorkspaceRootID(rootID string) string {
	rootID = sanitizeWorkspaceRootID(rootID)
	if rootID == "" {
		return "未分配"
	}
	if len(rootID) > 96 {
		return rootID[:96] + "…"
	}
	return rootID
}

// WorkspaceSelection stores the UI-only multi-selection. It is intentionally
// separate from authorization: the manager's List result is the active access
// scope, while selected entries are the next batch operation.
type WorkspaceSelection struct {
	selected map[string]struct{}
}

func NewWorkspaceSelection() WorkspaceSelection {
	return WorkspaceSelection{selected: make(map[string]struct{})}
}

func (s WorkspaceSelection) IsSelected(folder WorkspaceFolder) bool {
	_, ok := s.selected[workspaceFolderKey(folder)]
	return ok
}

// Toggle changes one checkbox and returns a new selection value. Invalid
// indexes are ignored by the UI helper and reported to callers that need a
// strict boundary through ToggleWorkspaceSelection.
func (s WorkspaceSelection) Toggle(folders []WorkspaceFolder, index int) (WorkspaceSelection, error) {
	if index < 0 || index >= len(folders) {
		return s, ErrWorkspaceSelectionIndex
	}
	result := NewWorkspaceSelection()
	for key := range s.selected {
		result.selected[key] = struct{}{}
	}
	key := workspaceFolderKey(folders[index])
	if key == "" {
		return result, ErrWorkspacePathInvalid
	}
	if _, ok := result.selected[key]; ok {
		delete(result.selected, key)
	} else {
		result.selected[key] = struct{}{}
	}
	return result, nil
}

// Selected returns the current selection in folder-list order. The returned
// slice is detached and safe for a manager batch call.
func (s WorkspaceSelection) Selected(folders []WorkspaceFolder) []string {
	paths := make([]string, 0, len(s.selected))
	for _, folder := range folders {
		if s.IsSelected(folder) {
			paths = append(paths, WorkspaceFolderSelectionID(folder))
		}
	}
	return paths
}

func workspacePathKey(path string) string {
	path = strings.TrimSpace(path)
	if ValidateWorkspacePath(path) != nil {
		return ""
	}
	return strings.ToLower(path)
}

// WorkspaceFolderSelectionID is the stable identifier sent to Remove. New
// entries should always have a RootID; path fallback preserves compatibility
// with older managers that have not assigned one yet.
func WorkspaceFolderSelectionID(folder WorkspaceFolder) string {
	if rootID := sanitizeWorkspaceRootID(folder.RootID); rootID != "" {
		return rootID
	}
	return strings.TrimSpace(folder.Path)
}

func workspaceFolderKey(folder WorkspaceFolder) string {
	if rootID := sanitizeWorkspaceRootID(folder.RootID); rootID != "" {
		return "id:" + strings.ToLower(rootID)
	}
	if path := workspacePathKey(folder.Path); path != "" {
		return "path:" + path
	}
	return ""
}

func sanitizeWorkspaceRootID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 || !utf8.ValidString(value) {
		return ""
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return ""
		}
	}
	return value
}

func minWorkspaceInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
