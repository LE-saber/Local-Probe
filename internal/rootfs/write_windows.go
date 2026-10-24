package rootfs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/LE-saber/Local-Probe/internal/policy"
	"golang.org/x/sys/windows"
)

const maxWriteBytes = 128 << 10

// WithDirectory pins a no-reparse, fixed-volume directory chain for the entire
// callback. The supplied name is presentation; handles anchor the actual root.
// Commands still have trusted-host permissions, not directory sandboxing.
func (s *Source) WithDirectory(b policy.BoundScope, id, relative string, call func(string) error) error {
	if call == nil || !b.AllowsRoot(id) || !b.AllowsDirectory(id, relative) {
		return ErrDenied
	}
	root, err := s.commandRoot(b, id)
	if err != nil {
		return ErrDenied
	}
	if err = rejectPresentedRoot(root); err != nil {
		return ErrDenied
	}
	anchor, err := root.Open(".")
	if err != nil {
		return ErrDenied
	}
	defer anchor.Close()
	identity, err := strongCommandIdentity(anchor, false)
	if err != nil {
		return ErrDenied
	}
	base, err := finalCommandPath(anchor)
	if err != nil || requireFixedCommandVolume(base) != nil {
		return ErrDenied
	}
	// OpenRoot follows links in its presented name. Reject an anchor that
	// resolved elsewhere during Source construction, even if that name was
	// restored before this operation. No pathname-only cwd/write fallback.
	if !sameCommandPath(base, `\\?\`+filepath.Clean(root.Name())) {
		return ErrDenied
	}
	full := base
	if relative != "" {
		full = joinCommandPath(base, relative)
	}
	// Pin every ancestor, including the configured root, against rename/delete.
	var handles []*os.File
	defer func() {
		for i := len(handles) - 1; i >= 0; i-- {
			handles[i].Close()
		}
	}()
	current := full[:7]
	parts := strings.Split(strings.Trim(full[7:], `\`), `\`)
	names := []string{current}
	for _, part := range parts {
		if part != "" {
			current = strings.TrimRight(current, `\`) + `\` + part
			names = append(names, current)
		}
	}
	matched := false
	for _, name := range names {
		p, er := windows.UTF16PtrFromString(name)
		if er != nil {
			return ErrDenied
		}
		h, er := windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
		if er != nil {
			return ErrDenied
		}
		f := os.NewFile(uintptr(h), "")
		handles = append(handles, f)
		actual, er := strongCommandIdentity(f, false)
		if er != nil {
			return ErrDenied
		}
		final, er := finalCommandPath(f)
		if er != nil || !sameCommandPath(final, name) {
			return ErrDenied
		}
		if sameCommandPath(name, base) {
			if actual.volume != identity.volume || actual.fileID != identity.fileID {
				return ErrDenied
			}
			matched = true
		}
	}
	if !matched || b.Validate() != nil {
		return ErrDenied
	}
	return call(strings.TrimPrefix(full, `\\?\`))
}

func (s *Source) openWriteFile(b policy.BoundScope, id, relative string, write, create bool, call func(*os.File) error) error {
	if !b.AllowsRoot(id) || !b.AllowsPath(id, relative) {
		return ErrDenied
	}
	directory := path.Dir(relative)
	if directory == "." {
		directory = ""
	}
	return s.WithDirectory(b, id, directory, func(parent string) error {
		name := strings.TrimRight(parent, `\`) + `\` + path.Base(relative)
		p, err := windows.UTF16PtrFromString(name)
		if err != nil {
			return ErrDenied
		}
		access := uint32(windows.GENERIC_READ)
		share := uint32(windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE)
		mode := uint32(windows.OPEN_EXISTING)
		if write {
			access |= windows.GENERIC_WRITE
			share = windows.FILE_SHARE_READ
		}
		if create {
			mode = windows.CREATE_NEW
		}
		if create {
			release, err := b.AcquireRevisionLease()
			if err != nil {
				return ErrDenied
			}
			defer release()
		}
		h, err := windows.CreateFile(p, access, share, nil, mode, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			return os.ErrNotExist
		}
		if errors.Is(err, windows.ERROR_FILE_EXISTS) || errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			return ErrWriteConflict
		}
		if err != nil {
			return ErrDenied
		}
		f := os.NewFile(uintptr(h), "")
		defer f.Close()
		if _, err = strongCommandIdentity(f, true); err != nil {
			return ErrDenied
		}
		actual, err := finalCommandPath(f)
		if err != nil || !sameCommandPath(actual, `\\?\`+name) {
			return ErrDenied
		}
		if !create && b.Validate() != nil {
			return ErrDenied
		}
		return call(f)
	})
}
func writeVersion(f *os.File) (string, []byte, error) {
	before, err := strongCommandIdentity(f, true)
	if err != nil {
		return "", nil, ErrDenied
	}
	data, err := io.ReadAll(io.LimitReader(f, maxWriteBytes+1))
	if err != nil || len(data) > maxWriteBytes {
		return "", nil, ErrDenied
	}
	meta, err := f.Stat()
	if err != nil || meta.Size() != int64(len(data)) {
		return "", nil, ErrWriteConflict
	}
	h := sha256.Sum256(data)
	encoded, _ := json.Marshal(struct {
		Volume, ID uint64
		Hash       string
	}{before.volume, before.fileID, hex.EncodeToString(h[:])})
	v := sha256.Sum256(encoded)
	return hex.EncodeToString(v[:]), data, nil
}
func (s *Source) FileVersion(b policy.BoundScope, id, relative string) (string, error) {
	v, _, err := s.FilePreview(b, id, relative)
	return v, err
}
func (s *Source) FilePreview(b policy.BoundScope, id, relative string) (string, []byte, error) {
	var version string
	var before []byte
	err := s.openWriteFile(b, id, relative, false, false, func(f *os.File) error { v, old, err := writeVersion(f); version = v; before = old; return err })
	if errors.Is(err, os.ErrNotExist) {
		return "absent", nil, nil
	}
	return version, before, err
}

// WriteFile updates through a no-write/no-delete-sharing handle. Existing-file
// updates are NOT atomic replacements: a durable private before image is written
// first. A failed update is reported unknown, never silently retried/restored.
func (s *Source) WriteFile(b policy.BoundScope, id, relative, expected string, data []byte, backup func([]byte) error) error {
	if !b.AllowsWrites() || backup == nil || len(data) > maxWriteBytes || expected == "" {
		return ErrDenied
	}
	create := expected == "absent"
	if create {
		if err := backup(nil); err != nil {
			return err
		}
	}
	return s.openWriteFile(b, id, relative, true, create, func(f *os.File) error {
		if !create {
			v, old, err := writeVersion(f)
			if err != nil {
				return err
			}
			if v != expected {
				return ErrWriteConflict
			}
			if err = backup(old); err != nil {
				return err
			}
		}
		if !create {
			release, err := b.AcquireRevisionLease()
			if err != nil {
				return ErrDenied
			}
			defer release()
		}
		var err error
		if _, err = f.Seek(0, 0); err != nil {
			return ErrWriteUncertain
		}
		if _, err = f.Write(data); err != nil {
			return ErrWriteUncertain
		}
		if err = f.Truncate(int64(len(data))); err != nil {
			return ErrWriteUncertain
		}
		if err = f.Sync(); err != nil {
			return ErrWriteUncertain
		}
		return nil
	})
}
