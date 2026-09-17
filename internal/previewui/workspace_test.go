package previewui

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type safeWorkspaceTestError struct{}

func (safeWorkspaceTestError) Error() string { return `raw C:\secret token=abc` }
func (safeWorkspaceTestError) WorkspaceUserMessage() string {
	return "文件夹授权被拒绝"
}
func (safeWorkspaceTestError) WorkspaceRemediation() string {
	return "检查权限后重试"
}

func TestNormalizeWorkspaceFoldersBoundsAndDeduplicates(t *testing.T) {
	values := []WorkspaceFolder{
		{RootID: "one", Path: `C:\Work`},
		{RootID: "ONE", Path: `c:\work`},
		{Path: "bad\npath"},
		{Path: strings.Repeat("x", MaxWorkspacePathBytes+1)},
		{Path: `D:\Other`},
	}
	got := NormalizeWorkspaceFolders(values)
	if len(got) != 2 || got[0].RootID != "one" || got[0].Path != `C:\Work` || got[1].Path != `D:\Other` {
		t.Fatalf("normalized folders = %#v", got)
	}
}

func TestWorkspaceSelectionSupportsMultipleEntriesAndCopiesState(t *testing.T) {
	folders := []WorkspaceFolder{{RootID: "one", Path: `C:\One`}, {RootID: "two", Path: `D:\Two`}, {RootID: "three", Path: `E:\Three`}}
	selection := NewWorkspaceSelection()
	var err error
	selection, err = selection.Toggle(folders, 0)
	if err != nil {
		t.Fatal(err)
	}
	selection, err = selection.Toggle(folders, 2)
	if err != nil {
		t.Fatal(err)
	}
	selected := selection.Selected(folders)
	if len(selected) != 2 || selected[0] != "one" || selected[1] != "three" {
		t.Fatalf("selected = %#v", selected)
	}
	selection, err = selection.Toggle(folders, 0)
	if err != nil || len(selection.Selected(folders)) != 1 || !selection.IsSelected(folders[2]) || selection.Selected(folders)[0] != "three" {
		t.Fatalf("toggle off failed: selection=%#v err=%v", selection, err)
	}
	if _, err := selection.Toggle(folders, 9); !errors.Is(err, ErrWorkspaceSelectionIndex) {
		t.Fatalf("invalid index error = %v", err)
	}
}

func TestWorkspacePathAndDisplayBoundaries(t *testing.T) {
	if !errors.Is(ValidateWorkspacePath(""), ErrWorkspacePathEmpty) {
		t.Fatal("empty path should be rejected")
	}
	if !errors.Is(ValidateWorkspacePath(strings.Repeat("x", MaxWorkspacePathBytes+1)), ErrWorkspacePathTooLong) {
		t.Fatal("long path should be rejected")
	}
	if !errors.Is(ValidateWorkspacePath("C:\\bad\npath"), ErrWorkspacePathInvalid) {
		t.Fatal("control characters should be rejected")
	}
	long := `C:\workspace\` + strings.Repeat("folder\\", 80) + "file.txt"
	display := WorkspaceDisplayPath(long)
	if len(display) > MaxWorkspaceDisplayBytes || !strings.Contains(display, "…") {
		t.Fatalf("display path is not bounded: len=%d value=%q", len(display), display)
	}
}

func TestWorkspaceErrorPresentationNeverUsesRawError(t *testing.T) {
	message, remediation := WorkspaceErrorPresentation(safeWorkspaceTestError{})
	if message != "文件夹授权被拒绝" || remediation != "检查权限后重试" {
		t.Fatalf("safe presentation = %q / %q", message, remediation)
	}
	message, remediation = WorkspaceErrorPresentation(errors.New(`C:\secret token=abc`))
	if message == "" || remediation == "" || strings.Contains(message, "secret") || strings.Contains(remediation, "token") {
		t.Fatalf("unsafe generic presentation = %q / %q", message, remediation)
	}
	message, _ = WorkspaceErrorPresentation(context.DeadlineExceeded)
	if message != "工作空间操作超时" {
		t.Fatalf("deadline presentation = %q", message)
	}
	message, remediation = WorkspaceErrorPresentation(errors.Join(ErrWorkspacePickerUnavailable, errors.New("raw COM detail")))
	if message != "无法打开文件夹选择器" || strings.Contains(remediation, "COM") {
		t.Fatalf("picker availability presentation = %q / %q", message, remediation)
	}
	message, remediation = WorkspaceErrorPresentation(ErrWorkspacePickerSelection)
	if message != "所选文件夹无效" || remediation == "" {
		t.Fatalf("picker selection presentation = %q / %q", message, remediation)
	}
}
