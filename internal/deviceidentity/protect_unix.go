//go:build !windows

package deviceidentity

import (
	"bytes"
	"io/fs"
	"os"
)

var protectionHeader = []byte("LPID1-PRIVATE-FILE\x00")

func protectDirectory(path string) error { return os.Chmod(path, 0700) }
func checkDirectory(info fs.FileInfo) error {
	if info.Mode().Perm()&0077 != 0 {
		return ErrStorage
	}
	return nil
}
func checkRecord(info fs.FileInfo) error {
	if info.Mode().Perm()&0077 != 0 {
		return ErrStorage
	}
	return nil
}

// Unix v1 relies on owner-only filesystem permissions; this is not encryption
// against the OS owner or root. Windows uses current-user DPAPI instead.
func seal(data []byte) ([]byte, error) {
	return append(append([]byte(nil), protectionHeader...), data...), nil
}
func unseal(data []byte) ([]byte, error) {
	if !bytes.HasPrefix(data, protectionHeader) {
		return nil, ErrStorage
	}
	return append([]byte(nil), data[len(protectionHeader):]...), nil
}
