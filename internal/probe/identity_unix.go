//go:build aix || android || darwin || dragonfly || freebsd || hurd || illumos || linux || netbsd || openbsd || solaris

package probe

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func validateExecutablePath(path string) error {
	if path == "" || strings.IndexByte(path, 0) >= 0 || !filepath.IsAbs(path) {
		return ErrInvalidInput
	}
	// Keep the audited spelling stable.  This rejects ., .., repeated
	// separators and trailing separators, which otherwise create aliases for
	// the same executable path.
	if filepath.Clean(path) != path {
		return ErrInvalidInput
	}
	return nil
}

func captureIdentity(path string) (fileIdentity, error) {
	if err := validateExecutablePath(path); err != nil {
		return fileIdentity{}, err
	}
	if err := rejectRemoteFilesystem(path); err != nil {
		return fileIdentity{}, err
	}
	if err := checkUnixPathComponents(path); err != nil {
		return fileIdentity{}, err
	}

	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		switch {
		case errors.Is(err, unix.ENOENT), errors.Is(err, unix.ENOTDIR):
			return fileIdentity{}, ErrNotFound
		case errors.Is(err, unix.ELOOP):
			return fileIdentity{}, ErrRejected
		default:
			return fileIdentity{}, ErrUnavailable
		}
	}
	file := os.NewFile(uintptr(fd), "")
	if file == nil {
		_ = unix.Close(fd)
		return fileIdentity{}, ErrUnavailable
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fileIdentity{}, ErrUnavailable
	}
	if !info.Mode().IsRegular() {
		return fileIdentity{}, ErrRejected
	}
	key := unixIdentityKey(info)
	if info.Sys() == nil || key == "" {
		return fileIdentity{}, ErrUnavailable
	}
	return fileIdentity{key: key, valid: true}, nil
}

func unixIdentityKey(info os.FileInfo) string {
	sys := info.Sys()
	if sys == nil {
		return ""
	}
	value := reflect.ValueOf(sys)
	for value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface {
		if value.IsNil() {
			return ""
		}
		value = value.Elem()
	}
	if value.Kind() == reflect.Struct {
		dev, devOK := unsignedStatField(value, "Dev")
		ino, inoOK := unsignedStatField(value, "Ino")
		if devOK && inoOK {
			return fmt.Sprintf("%T:%s:%s", sys, strconv.FormatUint(dev, 10), strconv.FormatUint(ino, 10))
		}
	}
	// A supported Unix port without Dev/Ino is safer to reject than to use a
	// mutable metadata field as an identity.  Keep this fallback only for
	// platforms whose os.FileInfo has a stable textual system identity.
	return fmt.Sprintf("%T:%#v", sys, sys)
}

func unsignedStatField(value reflect.Value, name string) (uint64, bool) {
	field := value.FieldByName(name)
	if !field.IsValid() {
		return 0, false
	}
	switch field.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return field.Uint(), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if field.Int() < 0 {
			return 0, false
		}
		return uint64(field.Int()), true
	default:
		return 0, false
	}
}

func checkUnixPathComponents(path string) error {
	current := string(filepath.Separator)
	components := strings.Split(strings.TrimPrefix(path, current), string(filepath.Separator))
	for index, component := range components {
		if component == "" {
			return ErrInvalidInput
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return ErrNotFound
			}
			return ErrUnavailable
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return ErrRejected
		}
		if index < len(components)-1 && !info.IsDir() {
			return ErrRejected
		}
	}
	return nil
}
