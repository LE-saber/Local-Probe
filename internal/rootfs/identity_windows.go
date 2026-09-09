//go:build windows

package rootfs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

const driveRemote = 4

var getDriveTypeW = syscall.NewLazyDLL("kernel32.dll").NewProc("GetDriveTypeW")

func nativeMetadata(file *os.File, _ os.FileInfo) (fileIdentity, error) {
	if file == nil {
		return fileIdentity{}, errors.New("nil file")
	}
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(file.Fd()), &info); err != nil {
		return fileIdentity{}, err
	}
	return fileIdentity{
		volume:      uint64(info.VolumeSerialNumber),
		fileID:      uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow),
		links:       uint64(info.NumberOfLinks),
		attributes:  uint64(info.FileAttributes),
		hasIdentity: info.VolumeSerialNumber != 0 || info.FileIndexHigh != 0 || info.FileIndexLow != 0,
		hasLinks:    info.NumberOfLinks != 0,
		reparse:     info.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0,
	}, nil
}

func fileInfoReparse(info os.FileInfo) bool {
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return ok && data.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
}

func validateRootPathPlatform(rootPath string) error {
	if strings.HasPrefix(rootPath, `\\`) || strings.HasPrefix(rootPath, `//`) {
		return ErrUnsupportedType
	}
	volume := filepath.VolumeName(rootPath)
	if len(volume) != 2 || volume[1] != ':' {
		return ErrInvalidRoot
	}
	driveType, err := windowsDriveType(volume + `\`)
	if err != nil {
		return ErrInvalidRoot
	}
	if driveType == driveRemote {
		return ErrUnsupportedType
	}
	return nil
}

func windowsDriveType(rootPath string) (uint32, error) {
	pathPtr, err := syscall.UTF16PtrFromString(rootPath)
	if err != nil {
		return 0, err
	}
	driveType, _, callErr := getDriveTypeW.Call(uintptr(unsafe.Pointer(pathPtr)))
	if driveType == 0 {
		if callErr != nil {
			return 0, callErr
		}
		return 0, syscall.EINVAL
	}
	return uint32(driveType), nil
}
