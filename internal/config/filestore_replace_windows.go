//go:build windows

package config

import (
	"errors"
	"golang.org/x/sys/windows"
	"time"
)

func replaceFile(oldPath, newPath string) error {
	oldName, err := windows.UTF16PtrFromString(oldPath)
	if err != nil {
		return err
	}
	newName, err := windows.UTF16PtrFromString(newPath)
	if err != nil {
		return err
	}
	return retryWindowsReplacement(func() error {
		return windows.MoveFileEx(oldName, newName, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
	}, time.Sleep)
}

// Windows can return access denied while a scanner temporarily opens a recently
// replaced destination without delete sharing. Retry the SAME atomic rename
// under the FileStore writer/CAS lock, without rewriting data or changing ACLs.
// Permanent denial and every unknown error remain failures.
func retryWindowsReplacement(replace func() error, pause func(time.Duration)) error {
	var err error
	for attempt := 0; attempt < 16; attempt++ {
		err = replace()
		if err == nil {
			return nil
		}
		if !errors.Is(err, windows.ERROR_ACCESS_DENIED) && !errors.Is(err, windows.ERROR_SHARING_VIOLATION) && !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return err
		}
		if attempt < 15 {
			pause(5 * time.Millisecond)
		}
	}
	return err
}
