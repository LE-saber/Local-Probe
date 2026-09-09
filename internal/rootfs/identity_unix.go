//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package rootfs

import (
	"os"
	"syscall"
)

func nativeMetadata(_ *os.File, info os.FileInfo) (fileIdentity, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileIdentity{}, syscall.EINVAL
	}
	return fileIdentity{
		volume:      uint64(stat.Dev),
		fileID:      uint64(stat.Ino),
		links:       uint64(stat.Nlink),
		hasIdentity: true,
		hasLinks:    true,
	}, nil
}

func fileInfoReparse(_ os.FileInfo) bool { return false }

func validateRootPathPlatform(_ string) error { return nil }
