//go:build !windows && !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package rootfs

import "os"

func nativeMetadata(_ *os.File, _ os.FileInfo) (fileIdentity, error) {
	return fileIdentity{}, nil
}

func fileInfoReparse(_ os.FileInfo) bool { return false }

func validateRootPathPlatform(_ string) error { return nil }
