//go:build windows

package rootfs

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unicode"
	"unicode/utf8"

	"github.com/LE-saber/Local-Probe/internal/commandpath"
	"github.com/LE-saber/Local-Probe/internal/policy"
	"golang.org/x/sys/windows"
)

const (
	// GetFinalPathNameByHandleW is bounded even before the launcher applies
	// its own command-line budget.  The extra UTF-16 cell is reserved for the
	// terminating NUL in the API buffer.
	maxCommandPathUTF16  = 32766
	maxCommandPathBuffer = maxCommandPathUTF16 + 1
)

// commandPathBinding owns both the Source-root anchor and the final file
// handle.  The absolute path is only a preview presentation of the final
// handle; it is never the security proof or launch authorization.  A future
// launcher must receive this binding and revalidate plus launch in one
// trusted operation; PreviewToken must never be used as a standalone argv
// string.  The object identity and root identity are checked again by
// Revalidate for that future handoff.
type commandPathBinding struct {
	mu sync.Mutex

	source   *Source
	bound    policy.BoundScope
	revision string
	rootID   string
	relative string

	root       *os.Root
	rootAnchor *os.File
	final      *os.File

	rootIdentity  fileIdentity
	finalIdentity fileIdentity
	rootPath      string
	finalPath     string
	commitment    [32]byte
	closed        bool
}

var _ commandpath.PathBinding = (*commandPathBinding)(nil)

// Resolve performs one trusted local resolution.  It returns a binding only
// after the path has passed policy, no-reparse component checks, strong
// Windows handle identity checks, and local fixed-volume validation.
func (r *commandPathResolver) Resolve(ctx context.Context, bound policy.BoundScope, rootID, relativePath string) (commandpath.PathBinding, error) {
	if r == nil || r.source == nil {
		return nil, commandpath.ErrUnavailable
	}
	if err := validateCommandPathRequest(ctx, bound, rootID, relativePath); err != nil {
		return nil, err
	}
	// BindCommandPath captures the authorization decision.  Do not allow a
	// caller to substitute another scope with the same root ID but different
	// profile metadata.
	if r.revision == "" || r.bound.ConnectionID() != bound.ConnectionID() ||
		r.bound.ProfileID() != bound.ProfileID() || r.revision != bound.Revision() {
		return nil, commandpath.ErrDenied
	}
	if err := validateCommandPathRequest(ctx, r.bound, rootID, relativePath); err != nil {
		return nil, err
	}
	root, err := r.source.commandRoot(r.bound, rootID)
	if err != nil {
		return nil, err
	}
	if err := commandPathContext(ctx); err != nil {
		return nil, err
	}
	if err := rejectPresentedRoot(root); err != nil {
		return nil, err
	}

	rootAnchor, err := root.Open(".")
	if err != nil {
		return nil, mapCommandPathOpenError(err)
	}
	cleanupAnchor := true
	defer func() {
		if cleanupAnchor {
			_ = rootAnchor.Close()
		}
	}()
	rootIdentity, err := strongCommandIdentity(rootAnchor, false)
	if err != nil {
		return nil, err
	}
	rootPath, err := finalCommandPath(rootAnchor)
	if err != nil {
		return nil, err
	}
	if err := requireFixedCommandVolume(rootPath); err != nil {
		return nil, err
	}

	if err := rejectCommandPathComponents(root, relativePath); err != nil {
		return nil, err
	}
	if err := commandPathContext(ctx); err != nil {
		return nil, err
	}
	final, err := root.Open(relativePath)
	if err != nil {
		return nil, mapCommandPathOpenError(err)
	}
	cleanupFinal := true
	defer func() {
		if cleanupFinal {
			_ = final.Close()
		}
	}()
	if err := rejectCommandPathComponents(root, relativePath); err != nil {
		return nil, err
	}
	finalIdentity, err := strongCommandIdentity(final, true)
	if err != nil {
		return nil, err
	}
	finalPath, err := finalCommandPath(final)
	if err != nil {
		return nil, err
	}
	if err := requireFixedCommandVolume(finalPath); err != nil {
		return nil, err
	}
	expectedPath := joinCommandPath(rootPath, relativePath)
	if !sameCommandPath(finalPath, expectedPath) || !withinCommandRoot(rootPath, finalPath) {
		return nil, commandpath.ErrIdentityChanged
	}
	if err := commandPathContext(ctx); err != nil {
		return nil, err
	}
	commitment := commandPathCommitment(r.bound, rootID, relativePath, rootPath, finalPath, rootIdentity, finalIdentity)
	binding := &commandPathBinding{
		source:        r.source,
		bound:         r.bound,
		revision:      r.revision,
		rootID:        rootID,
		relative:      relativePath,
		root:          root,
		rootAnchor:    rootAnchor,
		final:         final,
		rootIdentity:  rootIdentity,
		finalIdentity: finalIdentity,
		rootPath:      rootPath,
		finalPath:     finalPath,
		commitment:    commitment,
	}
	cleanupAnchor = false
	cleanupFinal = false
	return binding, nil
}

func commandPathContext(ctx context.Context) error {
	if ctx == nil {
		return commandpath.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func rejectCommandPathComponents(root *os.Root, relativePath string) error {
	if root == nil {
		return commandpath.ErrUnavailable
	}
	prefix := ""
	parts := strings.Split(relativePath, "/")
	for index, component := range parts {
		if prefix == "" {
			prefix = component
		} else {
			prefix += "/" + component
		}
		info, err := root.Lstat(prefix)
		if err != nil {
			return mapCommandPathStatError(err)
		}
		attributes, ok := info.Sys().(*syscall.Win32FileAttributeData)
		if !ok {
			return commandpath.ErrUnavailable
		}
		if info.Mode()&os.ModeSymlink != 0 || attributes.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return commandpath.ErrDenied
		}
		if index < len(parts)-1 && !info.IsDir() {
			return commandpath.ErrDenied
		}
	}
	return nil
}

// OpenRoot intentionally follows links in the root name.  Source.New also
// checks the presented root, but that check and OpenRoot are separate system
// calls.  Recheck the presented name here and reject any current reparse
// point; the rooted handle below remains the actual containment anchor.
func rejectPresentedRoot(root *os.Root) error {
	if root == nil || root.Name() == "" {
		return commandpath.ErrUnavailable
	}
	info, err := os.Lstat(root.Name())
	if err != nil {
		return mapCommandPathStatError(err)
	}
	attributes, ok := info.Sys().(*syscall.Win32FileAttributeData)
	if !ok {
		return commandpath.ErrUnavailable
	}
	if info.Mode()&os.ModeSymlink != 0 || attributes.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 || !info.IsDir() {
		return commandpath.ErrDenied
	}
	return nil
}

func strongCommandIdentity(file *os.File, regular bool) (fileIdentity, error) {
	if file == nil {
		return fileIdentity{}, commandpath.ErrUnavailable
	}
	info, err := file.Stat()
	if err != nil {
		return fileIdentity{}, mapCommandPathStatError(err)
	}
	if regular && !info.Mode().IsRegular() {
		return fileIdentity{}, commandpath.ErrDenied
	}
	if !regular && !info.IsDir() {
		return fileIdentity{}, commandpath.ErrDenied
	}
	identity, err := nativeMetadata(file, info)
	if err != nil {
		return fileIdentity{}, commandpath.ErrUnavailable
	}
	if identity.reparse {
		return fileIdentity{}, commandpath.ErrDenied
	}
	if !identity.hasIdentity || identity.volume == 0 || identity.fileID == 0 {
		return fileIdentity{}, commandpath.ErrUnavailable
	}
	if !identity.hasLinks {
		return fileIdentity{}, commandpath.ErrUnavailable
	}
	if identity.links != 1 {
		return fileIdentity{}, commandpath.ErrDenied
	}
	if fileType, err := windows.GetFileType(windows.Handle(file.Fd())); err != nil || fileType != windows.FILE_TYPE_DISK {
		return fileIdentity{}, commandpath.ErrDenied
	}
	return identity, nil
}

func finalCommandPath(file *os.File) (string, error) {
	if file == nil {
		return "", commandpath.ErrUnavailable
	}
	handle := windows.Handle(file.Fd())
	if handle == windows.InvalidHandle {
		return "", commandpath.ErrClosed
	}
	for size := uint32(256); size <= maxCommandPathBuffer; {
		buffer := make([]uint16, size)
		length, err := windows.GetFinalPathNameByHandle(handle, &buffer[0], size, 0)
		if err != nil {
			return "", commandpath.ErrUnavailable
		}
		// The API reports the copied length without the terminating NUL on
		// success.  A path whose length is exactly size-1 therefore still
		// fits in the buffer; accepting it keeps the advertised UTF-16
		// boundary at maxCommandPathUTF16 instead of rejecting one valid
		// character at the limit.  A too-small buffer reports a required
		// size at least as large as the supplied buffer, so length < size
		// remains the conservative success test.
		if length != 0 && length < size {
			path, ok := decodeFinalCommandPath(buffer[:length])
			if !ok {
				return "", commandpath.ErrUnavailable
			}
			return path, nil
		}
		if size >= maxCommandPathBuffer {
			return "", commandpath.ErrUnavailable
		}
		next := size * 2
		if next > maxCommandPathBuffer {
			next = maxCommandPathBuffer
		}
		size = next
	}
	return "", commandpath.ErrUnavailable
}

func decodeFinalCommandPath(encoded []uint16) (string, bool) {
	if len(encoded) == 0 {
		return "", false
	}
	// Validate surrogate pairing explicitly before converting, because
	// UTF16ToString replaces malformed pairs and that would hide an
	// indeterminate OS path.
	for i := 0; i < len(encoded); i++ {
		unit := encoded[i]
		if unit >= 0xd800 && unit <= 0xdbff {
			if i+1 >= len(encoded) || encoded[i+1] < 0xdc00 || encoded[i+1] > 0xdfff {
				return "", false
			}
			i++
			continue
		}
		if unit >= 0xdc00 && unit <= 0xdfff {
			return "", false
		}
	}
	path := windows.UTF16ToString(encoded)
	if !utf8.ValidString(path) || path == "" || strings.ContainsRune(path, 0) {
		return "", false
	}
	for _, r := range path {
		if unicode.IsControl(r) {
			return "", false
		}
	}
	if !strings.HasPrefix(strings.ToLower(path), `\\?\`) {
		return "", false
	}
	if len(path) < 7 || !isCommandDriveLetter(path[4]) || path[5] != ':' || path[6] != '\\' {
		return "", false
	}
	// VOLUME_NAME_DOS still must resolve to a drive path.  UNC, NT-device,
	// volume-GUID and alternate-data-stream spellings are not accepted.
	if strings.HasPrefix(strings.ToUpper(path[4:]), `UNC\`) || strings.Contains(path[6:], ":") {
		return "", false
	}
	clean := filepath.Clean(path)
	if !sameCommandPath(clean, path) {
		return "", false
	}
	return path, true
}

func requireFixedCommandVolume(path string) error {
	if len(path) < 7 || !isCommandDriveLetter(path[4]) || path[5] != ':' || path[6] != '\\' {
		return commandpath.ErrUnavailable
	}
	if err := requireFixedCommandMount(path[4:7]); err != nil {
		return err
	}
	// A directory mounted below a fixed drive can itself be a remote
	// volume.  Query the volume mount point for the actual path as well as
	// checking its drive letter so a local-looking mount cannot bypass the
	// remote-volume guard.
	mountPath, err := commandVolumeMountPath(path)
	if err != nil {
		return commandpath.ErrUnavailable
	}
	if err := requireFixedCommandMount(mountPath); err != nil {
		return err
	}
	return nil
}

func requireFixedCommandMount(root string) error {
	ptr, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return commandpath.ErrUnavailable
	}
	if windows.GetDriveType(ptr) != windows.DRIVE_FIXED {
		return commandpath.ErrUnsupported
	}
	return nil
}

func commandVolumeMountPath(extendedPath string) (string, error) {
	if len(extendedPath) < 7 || !strings.HasPrefix(strings.ToLower(extendedPath), `\\?\`) {
		return "", commandpath.ErrUnavailable
	}
	pathPtr, err := windows.UTF16PtrFromString(extendedPath[4:])
	if err != nil {
		return "", commandpath.ErrUnavailable
	}
	buffer := make([]uint16, maxCommandPathBuffer)
	if err := windows.GetVolumePathName(pathPtr, &buffer[0], uint32(len(buffer))); err != nil {
		return "", commandpath.ErrUnavailable
	}
	length := uint32(0)
	for length < uint32(len(buffer)) && buffer[length] != 0 {
		length++
	}
	if length == 0 || length >= uint32(len(buffer)) {
		return "", commandpath.ErrUnavailable
	}
	mountPath := windows.UTF16ToString(buffer[:length])
	if mountPath == "" || strings.ContainsRune(mountPath, 0) {
		return "", commandpath.ErrUnavailable
	}
	if !strings.HasSuffix(mountPath, `\`) {
		mountPath += `\`
	}
	return mountPath, nil
}

func isCommandDriveLetter(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func joinCommandPath(rootPath, relativePath string) string {
	rootPath = strings.TrimRight(rootPath, `\`)
	return rootPath + `\` + strings.ReplaceAll(relativePath, "/", `\`)
}

func trimCommandPath(rootPath string) string {
	if len(rootPath) <= 7 {
		return rootPath
	}
	return strings.TrimRight(rootPath, `\`)
}

func sameCommandPath(left, right string) bool {
	return strings.EqualFold(trimCommandPath(left), trimCommandPath(right))
}

func withinCommandRoot(rootPath, candidate string) bool {
	// Keep the drive-root spelling (\\?\C:\) for equality checks, but
	// remove its trailing separator before constructing a child prefix.
	// Otherwise a drive-root source would produce `\\?\C:\\` and reject
	// every legitimate child.
	rootPath = strings.TrimRight(rootPath, `\`)
	candidate = trimCommandPath(candidate)
	prefix := rootPath + `\`
	return len(candidate) > len(prefix) && strings.EqualFold(candidate[:len(prefix)], prefix)
}

func commandPathCommitment(bound policy.BoundScope, rootID, relativePath, rootPath, finalPath string, rootIdentity, finalIdentity fileIdentity) [32]byte {
	hash := sha256.New()
	writeCommandPathField(hash, "local-probe/command-path/v1")
	writeCommandPathField(hash, bound.ConnectionID())
	writeCommandPathField(hash, bound.ProfileID())
	writeCommandPathField(hash, bound.Revision())
	writeCommandPathField(hash, rootID)
	writeCommandPathField(hash, relativePath)
	writeCommandPathField(hash, rootPath)
	writeCommandPathField(hash, finalPath)
	writeCommandPathIdentity(hash, rootIdentity)
	writeCommandPathIdentity(hash, finalIdentity)
	var commitment [32]byte
	copy(commitment[:], hash.Sum(nil))
	return commitment
}

func writeCommandPathField(hash interface{ Write([]byte) (int, error) }, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = hash.Write(length[:])
	_, _ = hash.Write([]byte(value))
}

func writeCommandPathIdentity(hash interface{ Write([]byte) (int, error) }, identity fileIdentity) {
	var value [24]byte
	binary.BigEndian.PutUint64(value[0:8], identity.volume)
	binary.BigEndian.PutUint64(value[8:16], identity.fileID)
	binary.BigEndian.PutUint64(value[16:24], identity.links)
	_, _ = hash.Write(value[:])
}

// PreviewToken returns the normalized path represented by the already-open
// final handle for local diagnostics or UI preview.  It is intentionally not
// a launch API: callers must not pass this string to an executor or use it as
// authorization.  A future launcher must accept the binding, revalidate it,
// and launch within one trusted operation.
func (b *commandPathBinding) PreviewToken() (string, error) {
	if err := b.Revalidate(context.Background()); err != nil {
		return "", err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.finalPath == "" {
		return "", commandpath.ErrClosed
	}
	return b.finalPath, nil
}

func (b *commandPathBinding) Commitment() [32]byte {
	if b == nil {
		return [32]byte{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return [32]byte{}
	}
	return b.commitment
}

func (b *commandPathBinding) Revalidate(ctx context.Context) error {
	if err := commandPathContext(ctx); err != nil {
		return err
	}
	if b == nil {
		return commandpath.ErrClosed
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.source == nil || b.root == nil || b.rootAnchor == nil || b.final == nil {
		return commandpath.ErrClosed
	}
	if err := validateCommandPathRequest(ctx, b.bound, b.rootID, b.relative); err != nil {
		return err
	}
	root, err := b.source.commandRoot(b.bound, b.rootID)
	if err != nil {
		return err
	}
	if root != b.root {
		return commandpath.ErrIdentityChanged
	}
	// Recheck the presented root name as well as the owned root handle.  This
	// does not close Source.New's original Lstat-to-OpenRoot construction race;
	// it is an additional fail-closed check for an already-created binding.
	if err := rejectPresentedRoot(root); err != nil {
		return err
	}
	if err := commandPathContext(ctx); err != nil {
		return err
	}
	rootIdentity, err := strongCommandIdentity(b.rootAnchor, false)
	if err != nil {
		return err
	}
	if !sameCommandIdentity(rootIdentity, b.rootIdentity) {
		return commandpath.ErrIdentityChanged
	}
	rootPath, err := finalCommandPath(b.rootAnchor)
	if err != nil {
		return err
	}
	if !sameCommandPath(rootPath, b.rootPath) {
		return commandpath.ErrIdentityChanged
	}
	if err := requireFixedCommandVolume(rootPath); err != nil {
		return err
	}
	if err := rejectCommandPathComponents(b.root, b.relative); err != nil {
		return err
	}
	if err := commandPathContext(ctx); err != nil {
		return err
	}
	current, err := b.root.Open(b.relative)
	if err != nil {
		return mapCommandPathOpenError(err)
	}
	defer current.Close()
	if err := rejectCommandPathComponents(b.root, b.relative); err != nil {
		return err
	}
	currentIdentity, err := strongCommandIdentity(current, true)
	if err != nil {
		return err
	}
	if !sameCommandIdentity(currentIdentity, b.finalIdentity) {
		return commandpath.ErrIdentityChanged
	}
	currentPath, err := finalCommandPath(current)
	if err != nil {
		return err
	}
	if err := requireFixedCommandVolume(currentPath); err != nil {
		return err
	}
	if !sameCommandPath(currentPath, b.finalPath) || !sameCommandPath(currentPath, joinCommandPath(rootPath, b.relative)) || !withinCommandRoot(rootPath, currentPath) {
		return commandpath.ErrIdentityChanged
	}
	return commandPathContext(ctx)
}

func sameCommandIdentity(left, right fileIdentity) bool {
	return left.hasIdentity && right.hasIdentity && left.hasLinks && right.hasLinks && left.links == 1 && right.links == 1 &&
		left.volume == right.volume && left.fileID == right.fileID && !left.reparse && !right.reparse
}

func (b *commandPathBinding) Close() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	final, anchor := b.final, b.rootAnchor
	b.final, b.rootAnchor = nil, nil
	b.root, b.source = nil, nil
	b.mu.Unlock()

	var closeFailed bool
	if final != nil {
		if err := final.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			closeFailed = true
		}
	}
	if anchor != nil {
		if err := anchor.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			closeFailed = true
		}
	}
	if closeFailed {
		return commandpath.ErrUnavailable
	}
	return nil
}

func (b *commandPathBinding) MarshalJSON() ([]byte, error) {
	return nil, commandpath.ErrLocalOnly
}

func (*commandPathBinding) UnmarshalJSON([]byte) error {
	return commandpath.ErrLocalOnly
}

func mapCommandPathStatError(err error) error {
	if errors.Is(err, os.ErrClosed) {
		return commandpath.ErrClosed
	}
	if errors.Is(err, fs.ErrPermission) {
		return commandpath.ErrDenied
	}
	return commandpath.ErrUnavailable
}

func mapCommandPathOpenError(err error) error {
	if errors.Is(err, os.ErrClosed) {
		return commandpath.ErrClosed
	}
	if errors.Is(err, fs.ErrPermission) {
		return commandpath.ErrDenied
	}
	return commandpath.ErrUnavailable
}
