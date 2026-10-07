//go:build windows

package supportbundle

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

func tightenFilePermissions(path string) error {
	// Break inheritance and grant full control only to the file owner.  This
	// is the Windows equivalent of chmod 0600; the descriptor is copied into
	// the file by SetNamedSecurityInfo before the atomic move.
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;OW)")
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil)
}

func atomicNoReplace(from, to string) error {
	fromName, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	toName, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	// Omitting MOVEFILE_REPLACE_EXISTING is the no-overwrite guarantee.
	return windows.MoveFileEx(fromName, toName, windows.MOVEFILE_WRITE_THROUGH)
}
