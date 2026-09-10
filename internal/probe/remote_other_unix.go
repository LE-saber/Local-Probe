//go:build aix || android || darwin || dragonfly || freebsd || hurd || illumos || netbsd || openbsd || solaris

package probe

func rejectRemoteFilesystem(string) error { return nil }
