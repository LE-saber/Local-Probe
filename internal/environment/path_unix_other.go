//go:build (aix || android || darwin || dragonfly || freebsd || hurd || illumos || netbsd || openbsd || solaris) && !linux

package environment

// validateLocalFilesystem is deliberately conservative on Unix ports whose
// filesystem magic values are not shared with Linux. The regular-file and
// no-symlink checks still apply; a future port should add native remote-FS
// detection before enabling remote discovery there.
func validateLocalFilesystem(string) error { return nil }
