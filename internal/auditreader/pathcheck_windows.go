//go:build windows

package auditreader

import (
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

func fileInfoReparse(info os.FileInfo) bool {
	if info == nil {
		return true
	}
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return !ok || data.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
}

// openAuditFile asks Windows to open the directory entry itself rather than
// traversing a reparse point.  The caller still validates Lstat before and
// after the read, because a directory entry may be swapped concurrently.
func openAuditFile(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, windows.ERROR_INVALID_HANDLE
	}
	return file, nil
}
