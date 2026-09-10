//go:build linux

package probe

import "golang.org/x/sys/unix"

// rejectRemoteFilesystem covers the common Linux network filesystem magic
// values.  An unknown local filesystem remains subject to the regular-file
// and no-symlink checks; it is not silently treated as a network share.
func rejectRemoteFilesystem(path string) error {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return ErrUnavailable
	}
	switch uint64(stat.Type) {
	case 0x00006969, // NFS
		0x0000FF53_4D42, // CIFS
		0x0000FE53_4D42, // SMB2
		0x00005346_414F, // AFS
		0x00007375_7245, // CODA
		0x00011619_70,   // GFS2
		0x0000C364_00:   // CEPH
		return ErrRejected
	default:
		return nil
	}
}
