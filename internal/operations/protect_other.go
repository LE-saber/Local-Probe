//go:build !windows

package operations

import (
	"golang.org/x/sys/unix"
	"io/fs"
	"os"
)

type fsLock struct{ file *os.File }

func protectJournal(path string) error { return os.Chmod(path, 0700) }
func checkPrivateJournal(_ string, i fs.FileInfo) error {
	if i.Mode().Perm()&0077 != 0 {
		return ErrStorage
	}
	return nil
}
func sealJournal(b []byte) ([]byte, error)   { return append([]byte(nil), b...), nil }
func unsealJournal(b []byte) ([]byte, error) { return append([]byte(nil), b...), nil }
func lockEngine(f *fsLock) error             { return unix.Flock(int(f.file.Fd()), unix.LOCK_EX|unix.LOCK_NB) }
