//go:build windows

package workspaceadmin

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

const (
	driveFixed  = 3
	driveRemote = 4
)

var getDriveTypeW = syscall.NewLazyDLL("kernel32.dll").NewProc("GetDriveTypeW")

func isCaseInsensitiveFS() bool { return true }

func validateExistingWorkspaceDirectory(raw string) (string, string, error) {
	if raw == "" || len(raw) > maxWorkspacePath || strings.ContainsRune(raw, 0) || !filepath.IsAbs(raw) {
		return "", "", problem(CodeInvalidPath, "选择一个绝对路径下的本地文件夹。", ErrInvalidPath)
	}
	if strings.HasPrefix(raw, `\\?\`) || strings.HasPrefix(raw, `\\.\`) || strings.HasPrefix(raw, `//`) || strings.HasPrefix(raw, `\\`) {
		return "", "", problem(CodeRemotePath, "UNC、设备路径和网络共享路径不可作为工作空间，请选择本机磁盘文件夹。", ErrRemotePath)
	}
	path := filepath.Clean(raw)
	volume := filepath.VolumeName(path)
	if len(volume) != 2 || volume[1] != ':' {
		return "", "", problem(CodeInvalidPath, "请选择本机固定磁盘上的绝对文件夹路径。", ErrInvalidPath)
	}
	if isDriveRoot(path, volume) {
		return "", "", problem(CodeDriveRoot, "不要授权整个磁盘根目录，请选择更具体的项目文件夹。", ErrDriveRoot)
	}
	if err := rejectRemoteDrive(volume); err != nil {
		return "", "", err
	}
	if err := rejectLinkComponents(path, volume); err != nil {
		return "", "", err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", "", problem(CodePathMissing, "先创建该文件夹，或选择一个已经存在的文件夹。", ErrPathMissing)
	}
	if err != nil {
		return "", "", problem(CodeInvalidPath, "确认当前账户可以访问该文件夹。", ErrInvalidPath)
	}
	if isReparse(info) || info.Mode()&os.ModeSymlink != 0 {
		return "", "", problem(CodePathLink, "符号链接、junction 或其他 reparse 点不能作为工作空间根目录，请选择实际文件夹。", ErrPathLink)
	}
	if !info.IsDir() {
		return "", "", problem(CodeNotDirectory, "请选择文件夹，而不是文件或特殊文件。", ErrNotDirectory)
	}
	key := strings.ToLower(filepath.Clean(path))
	return path, key, nil
}

func isDriveRoot(path, volume string) bool {
	rest := strings.TrimPrefix(path, volume)
	return strings.Trim(rest, `\/`) == ""
}

func rejectRemoteDrive(volume string) error {
	root := volume + `\`
	ptr, err := syscall.UTF16PtrFromString(root)
	if err != nil {
		return problem(CodeRemotePath, "无法确认磁盘类型，请选择本机固定磁盘上的文件夹。", ErrRemotePath)
	}
	driveType, _, callErr := getDriveTypeW.Call(uintptr(unsafe.Pointer(ptr)))
	if driveType == 0 {
		if callErr != nil {
			return problem(CodeRemotePath, "无法确认磁盘类型，请选择本机固定磁盘上的文件夹。", ErrRemotePath)
		}
		return problem(CodeRemotePath, "无法确认磁盘类型，请选择本机固定磁盘上的文件夹。", ErrRemotePath)
	}
	if uint32(driveType) == driveRemote {
		return problem(CodeRemotePath, "映射网络盘不能作为工作空间，请选择本机固定磁盘上的文件夹。", ErrRemotePath)
	}
	if uint32(driveType) != driveFixed {
		return problem(CodeNonFixedDrive, "工作空间必须位于本机固定磁盘，请选择固定磁盘上的文件夹。", ErrNonFixedDrive)
	}
	return nil
}

func rejectLinkComponents(path, volume string) error {
	current := volume + `\`
	rest := strings.TrimPrefix(path, volume)
	for _, part := range strings.FieldsFunc(rest, func(r rune) bool { return r == '\\' || r == '/' }) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return problem(CodePathMissing, "先创建该文件夹，或选择一个已经存在的文件夹。", ErrPathMissing)
		}
		if err != nil {
			return problem(CodeInvalidPath, "确认当前账户可以访问该文件夹。", ErrInvalidPath)
		}
		if isReparse(info) || info.Mode()&os.ModeSymlink != 0 {
			return problem(CodePathLink, "符号链接、junction 或其他 reparse 点不能作为工作空间根目录，请选择实际文件夹。", ErrPathLink)
		}
	}
	return nil
}

func isReparse(info os.FileInfo) bool {
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return ok && data.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
}
