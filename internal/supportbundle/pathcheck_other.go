//go:build !windows

package supportbundle

import "os"

func fileInfoReparse(info os.FileInfo) bool { return false }

func tightenFilePermissions(path string) error { return os.Chmod(path, 0600) }

func atomicNoReplace(from, to string) error {
	// Link creates the destination atomically and fails if it already exists;
	// unlinking the same-directory temporary name then removes the staging
	// reference without exposing a partially written file.
	if err := os.Link(from, to); err != nil {
		return err
	}
	if err := os.Remove(from); err != nil {
		return err
	}
	return nil
}
