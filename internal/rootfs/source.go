// Package rootfs provides the read-only filesystem boundary for readcore.
//
// Source instances are built from one immutable config.Store snapshot. A
// Source is used with a policy.BoundScope; the scope is deliberately retained
// by each Handle so revocation is checked for every operation.
package rootfs

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/LE-saber/Local-Probe/internal/config"
	"github.com/LE-saber/Local-Probe/internal/policy"
	"github.com/LE-saber/Local-Probe/internal/readcore"
)

var (
	// ErrDenied is returned for an out-of-scope, denied, revoked or linked
	// path. It also unwraps to fs.ErrPermission for readcore classification.
	ErrDenied = errors.New("filesystem access denied")
	// ErrUnsupportedType identifies directories and non-regular filesystem
	// objects. It intentionally carries no OS path.
	ErrUnsupportedType = errors.New("unsupported filesystem object")
	// ErrClosed identifies a Source or Handle after Close.
	ErrClosed = errors.New("filesystem source closed")
	// ErrInvalidRoot identifies an administrator root that is not an absolute
	// directory or cannot be opened safely.
	ErrInvalidRoot = errors.New("invalid filesystem root")
	// ErrUnavailable is a path-free fallback for unexpected filesystem errors.
	ErrUnavailable = errors.New("filesystem operation unavailable")
)

// Source owns one *os.Root per root in the current configuration snapshot.
// Root paths never come from a read request.
type Source struct {
	mu       sync.RWMutex
	roots    map[string]*os.Root
	revision string
	closed   bool
}

// New opens every root from the current config.Store snapshot. The store is
// not consulted after construction; a later config revision revokes existing
// BoundScopes and callers construct a new Source for the new snapshot.
func New(store *config.Store) (*Source, error) {
	if store == nil {
		return nil, ErrInvalidRoot
	}
	snapshot := store.Snapshot()
	configured := snapshot.Config().Roots()
	if len(configured) == 0 {
		return nil, ErrInvalidRoot
	}

	opened := make(map[string]*os.Root, len(configured))
	cleanup := func() {
		for _, root := range opened {
			_ = root.Close()
		}
	}
	for _, configuredRoot := range configured {
		if configuredRoot.ID() == "" {
			cleanup()
			return nil, ErrInvalidRoot
		}
		if _, exists := opened[configuredRoot.ID()]; exists {
			cleanup()
			return nil, ErrInvalidRoot
		}
		rootPath, err := validateRootPath(configuredRoot.Path())
		if err != nil {
			cleanup()
			return nil, err
		}
		root, err := os.OpenRoot(rootPath)
		if err != nil {
			cleanup()
			return nil, mapRootError(err)
		}
		opened[configuredRoot.ID()] = root
	}
	return &Source{roots: opened, revision: snapshot.Revision()}, nil
}

// Bind creates the readcore adapter for one authenticated BoundScope. The
// scope must belong to the same configuration snapshot used to build Source;
// this prevents a root ID reused by a later revision from addressing an old
// *os.Root.
func (s *Source) Bind(bound policy.BoundScope) (readcore.Source, error) {
	if s == nil {
		return nil, ErrClosed
	}
	if err := bound.Validate(); err != nil {
		return nil, deniedError()
	}
	s.mu.RLock()
	closed := s.closed
	revision := s.revision
	s.mu.RUnlock()
	if closed {
		return nil, ErrClosed
	}
	if revision == "" || bound.Revision() != revision {
		return nil, deniedError()
	}
	return &boundSource{source: s, bound: bound}, nil
}

// Open opens one regular file beneath the root named by ref. The
// policy.BoundScope is the authority, not model-supplied root/path data; it
// is validated here before and on every later Handle operation.
func (s *Source) Open(ctx context.Context, bound policy.BoundScope, ref readcore.FileRef) (readcore.Handle, error) {
	if s == nil {
		return nil, ErrClosed
	}
	if ctx == nil {
		return nil, ErrDenied
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := authorize(bound, ref); err != nil {
		return nil, err
	}
	s.mu.RLock()
	revision := s.revision
	s.mu.RUnlock()
	if revision == "" || bound.Revision() != revision {
		return nil, deniedError()
	}

	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return nil, ErrClosed
	}
	root := s.roots[ref.RootID]
	s.mu.RUnlock()
	if root == nil {
		return nil, deniedError()
	}

	if err := rejectSymlinkComponents(root, ref.Path); err != nil {
		return nil, err
	}
	file, err := root.Open(ref.Path)
	if err != nil {
		return nil, mapOpenError(err)
	}
	closeOnError := func(err error) (readcore.Handle, error) {
		_ = file.Close()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return closeOnError(err)
	}
	// Recheck the path after opening to catch ordinary link replacement. The
	// remaining adversarial race is bounded by os.Root's containment guarantee;
	// local administrators/owners are outside this process boundary.
	if err := rejectSymlinkComponents(root, ref.Path); err != nil {
		return closeOnError(err)
	}
	if _, err := metadataFor(file, ref.RootID); err != nil {
		return closeOnError(err)
	}
	return &fileHandle{file: file, bound: bound, ref: ref}, nil
}

// Close is idempotent. Existing handles retain their opened file until their
// own Close; no new Open succeeds after Source.Close.
func (s *Source) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	roots := make([]*os.Root, 0, len(s.roots))
	for _, root := range s.roots {
		roots = append(roots, root)
	}
	s.mu.Unlock()

	var closeErr error
	for _, root := range roots {
		if err := root.Close(); err != nil {
			closeErr = errors.Join(closeErr, ErrUnavailable)
		}
	}
	return closeErr
}

type fileHandle struct {
	file   *os.File
	bound  policy.BoundScope
	ref    readcore.FileRef
	closed atomic.Bool
}

var _ readcore.Handle = (*fileHandle)(nil)

// boundSource is the only adapter exposed to readcore. It refuses a raw
// scope with a different connection or revision before delegating to Source,
// which still revalidates the BoundScope on every operation.
type boundSource struct {
	source *Source
	bound  policy.BoundScope
}

var _ readcore.Source = (*boundSource)(nil)

func (s *boundSource) Open(ctx context.Context, scope readcore.Scope, ref readcore.FileRef) (readcore.Handle, error) {
	if s == nil || s.source == nil {
		return nil, ErrClosed
	}
	if err := s.bound.Validate(); err != nil {
		return nil, deniedError()
	}
	if scope.ConnectionID() != s.bound.ConnectionID() || scope.ProfileRevision() != s.bound.Revision() {
		return nil, deniedError()
	}
	return s.source.Open(ctx, s.bound, ref)
}

func (h *fileHandle) ReadAt(p []byte, offset int64) (int, error) {
	if h == nil || h.closed.Load() {
		return 0, ErrClosed
	}
	if err := h.authorize(); err != nil {
		return 0, err
	}
	if offset < 0 {
		return 0, ErrDenied
	}
	n, err := h.file.ReadAt(p, offset)
	if errors.Is(err, os.ErrClosed) {
		return n, ErrClosed
	}
	return n, err
}

func (h *fileHandle) Metadata(ctx context.Context) (readcore.Metadata, error) {
	if h == nil || h.closed.Load() {
		return readcore.Metadata{}, ErrClosed
	}
	if ctx == nil {
		return readcore.Metadata{}, ErrDenied
	}
	if err := ctx.Err(); err != nil {
		return readcore.Metadata{}, err
	}
	if err := h.authorize(); err != nil {
		return readcore.Metadata{}, err
	}
	metadata, err := metadataFor(h.file, h.ref.RootID)
	if err != nil {
		return readcore.Metadata{}, err
	}
	if err := ctx.Err(); err != nil {
		return readcore.Metadata{}, err
	}
	return metadata, nil
}

func (h *fileHandle) Close() error {
	if h == nil || h.closed.Swap(true) {
		return nil
	}
	if err := h.file.Close(); err != nil {
		return ErrUnavailable
	}
	return nil
}

func (h *fileHandle) authorize() error {
	if h.closed.Load() {
		return ErrClosed
	}
	return authorize(h.bound, h.ref)
}

func authorize(bound policy.BoundScope, ref readcore.FileRef) error {
	if err := bound.Validate(); err != nil {
		return deniedError()
	}
	if !bound.AllowsPath(ref.RootID, ref.Path) {
		return deniedError()
	}
	return nil
}

func rejectSymlinkComponents(root *os.Root, relativePath string) error {
	prefix := ""
	var finalInfo os.FileInfo
	for _, component := range strings.Split(relativePath, "/") {
		if prefix == "" {
			prefix = component
		} else {
			prefix += "/" + component
		}
		info, err := root.Lstat(prefix)
		if err != nil {
			return mapStatError(err)
		}
		finalInfo = info
		if info.Mode()&os.ModeSymlink != 0 || fileInfoReparse(info) {
			return deniedError()
		}
	}
	if finalInfo == nil || !finalInfo.Mode().IsRegular() {
		return unsupportedTypeError()
	}
	return nil
}

func unsupportedTypeError() error {
	return errors.Join(ErrUnsupportedType, &readcore.ItemError{
		Code:    "unsupported_type",
		Message: "file type is not supported",
	})
}

func validateRootPath(rootPath string) (string, error) {
	if rootPath == "" || !filepath.IsAbs(rootPath) {
		return "", ErrInvalidRoot
	}
	rootPath = filepath.Clean(rootPath)
	if err := validateRootPathPlatform(rootPath); err != nil {
		return "", err
	}
	info, err := os.Lstat(rootPath)
	if err != nil {
		return "", ErrInvalidRoot
	}
	if info.Mode()&os.ModeSymlink != 0 || fileInfoReparse(info) || !info.IsDir() {
		return "", ErrInvalidRoot
	}
	return rootPath, nil
}

func deniedError() error { return errors.Join(ErrDenied, fs.ErrPermission) }

func mapRootError(err error) error {
	if errors.Is(err, fs.ErrPermission) {
		return deniedError()
	}
	return ErrInvalidRoot
}

func mapStatError(err error) error {
	if errors.Is(err, fs.ErrPermission) {
		return deniedError()
	}
	if errors.Is(err, fs.ErrNotExist) {
		return fs.ErrNotExist
	}
	if errors.Is(err, os.ErrClosed) {
		return ErrClosed
	}
	return ErrUnavailable
}

func mapOpenError(err error) error {
	if errors.Is(err, fs.ErrPermission) {
		return deniedError()
	}
	if errors.Is(err, fs.ErrNotExist) {
		return fs.ErrNotExist
	}
	if errors.Is(err, os.ErrClosed) {
		return ErrClosed
	}
	return ErrUnavailable
}
