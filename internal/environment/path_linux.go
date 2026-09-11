//go:build linux

package environment

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// validateLocalFilesystem rejects common remote and kernel pseudo-filesystems
// before a candidate is considered. An absent final candidate is checked
// against its nearest existing parent so it cannot hide a remote location.
func validateLocalFilesystem(path string) error {
	existing := path
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return ErrUnavailable
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return ErrUnavailable
		}
		existing = parent
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(existing, &stat); err != nil {
		return ErrUnavailable
	}
	switch uint64(stat.Type) {
	case 0x00006969, // NFS
		0x0000FF53_4D42, // CIFS
		0x0000FE53_4D42, // SMB2
		0x00005346_414F, // AFS
		0x00007375_7245, // CODA
		0x00011619_70,   // GFS2
		0x0000C364_00,   // CEPH
		0x00009FA0,      // procfs
		0x62656572,      // sysfs
		0x00001CD1,      // devpts
		0x00027E0EB,     // cgroup
		0x63677270:      // cgroup2
		return ErrRejectedCandidate
	default:
		return nil
	}
}
