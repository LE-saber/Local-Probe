//go:build !windows

package workspaceadmin

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func isCaseInsensitiveFS() bool { return false }

func validateExistingWorkspaceDirectory(raw string) (string, string, error) {
	if raw == "" || len(raw) > maxWorkspacePath || strings.ContainsRune(raw, 0) || !filepath.IsAbs(raw) {
		return "", "", problem(CodeInvalidPath, "选择一个绝对路径下的本地文件夹。", ErrInvalidPath)
	}
	path := filepath.Clean(raw)
	if path == string(filepath.Separator) {
		return "", "", problem(CodeDriveRoot, "不要授权文件系统根目录，请选择更具体的项目文件夹。", ErrDriveRoot)
	}
	if strings.HasPrefix(path, "//") {
		return "", "", problem(CodeRemotePath, "网络共享路径不可作为工作空间，请选择本机磁盘上的文件夹。", ErrRemotePath)
	}
	if err := rejectLinkComponents(path); err != nil {
		return "", "", err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", "", problem(CodePathMissing, "先创建该文件夹，或选择一个已经存在的文件夹。", ErrPathMissing)
	}
	if err != nil {
		return "", "", problem(CodeInvalidPath, "确认当前账户可以访问该文件夹。", ErrInvalidPath)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", "", problem(CodePathLink, "符号链接不能作为工作空间根目录，请选择其实际的普通文件夹。", ErrPathLink)
	}
	if !info.IsDir() {
		return "", "", problem(CodeNotDirectory, "请选择文件夹，而不是文件或特殊文件。", ErrNotDirectory)
	}
	key := filepath.Clean(path)
	return path, key, nil
}

func rejectLinkComponents(path string) error {
	current := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator)) {
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
		if info.Mode()&os.ModeSymlink != 0 {
			return problem(CodePathLink, "符号链接不能作为工作空间根目录，请选择其实际的普通文件夹。", ErrPathLink)
		}
	}
	return nil
}
