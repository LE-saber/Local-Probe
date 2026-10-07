package config

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

// FileStore persists the validated, non-secret Config value at one explicitly
// selected path. It does not choose a default path, expand environment
// variables, or resolve credential references.
//
// A FileStore is safe for concurrent use. The writer lock is process-local;
// applications that may have more than one process writing the same file
// must add an operating-system-level lock around the process boundary.
// It is deliberately not a production authorization/configuration boundary:
// ancestor reparse/TOCTOU, hardlink, ACL and cross-process locking guarantees
// are outside this helper. Callers must check RequireProductionReady before
// any runtime or authorization adapter wiring.
type FileStore struct {
	path         string
	backupPath   string
	createParent bool
	fs           fileStoreFS
	locks        []*fileStorePathLock
}

// FileStoreOptions describes the only path forms accepted by
// NewFileStoreWithOptions. Path is an explicit file path. Alternatively,
// ParentDir plus FileName describes one direct child of an explicit parent;
// FileName cannot contain a separator or traversal component.
//
// Parent directories are not created unless CreateParent is true. This keeps
// a typo in an explicit path from silently creating a new directory tree.
type FileStoreOptions struct {
	Path         string
	ParentDir    string
	FileName     string
	CreateParent bool
}

var (
	// ErrFileStorePath identifies a path rejected before filesystem access.
	ErrFileStorePath = errors.New("invalid configuration file path")
	// ErrFileStoreIO identifies a filesystem operation failure. The wrapped
	// error intentionally contains no path or operating-system detail.
	ErrFileStoreIO = errors.New("configuration storage operation failed")
	// ErrConfigNotFound means the configured file does not exist.
	ErrConfigNotFound = errors.New("configuration file not found")
	// ErrBackupNotFound means the store's explicitly managed backup does not
	// exist. A restore never searches arbitrary paths.
	ErrBackupNotFound = errors.New("configuration backup not found")
	// ErrUnsafeForProduction is returned by the explicit production gate. The
	// FileStore is a trusted private-directory persistence convenience only;
	// it is not a path-authority, hardlink/reparse defense, or cross-process CAS.
	ErrUnsafeForProduction = errors.New("configuration filestore is not production-ready")
)

var (
	fileStoreLocksMu sync.Mutex
	fileStoreLocks   = make(map[string]*fileStorePathLock)
)

type fileStorePathLock struct {
	mu sync.RWMutex
}

func (s *FileStore) lockResources(write bool) func() {
	for _, lock := range s.locks {
		if write {
			lock.mu.Lock()
		} else {
			lock.mu.RLock()
		}
	}
	return func() {
		for i := len(s.locks) - 1; i >= 0; i-- {
			if write {
				s.locks[i].mu.Unlock()
			} else {
				s.locks[i].mu.RUnlock()
			}
		}
	}
}

func uniqueStrings(values []string) []string {
	if len(values) < 2 || values[0] != values[1] {
		return values
	}
	return values[:1]
}

// NewFileStore creates a store for one explicit absolute configuration path.
// The parent directory must already exist; use NewFileStoreWithOptions with
// CreateParent when creation of that exact explicit parent is desired.
func NewFileStore(path string) (*FileStore, error) {
	return NewFileStoreWithOptions(FileStoreOptions{Path: path})
}

// NewFileStoreIn creates a store for the direct child fileName of parentDir.
// This is useful for callers that have an explicitly selected directory while
// still preventing a filename from escaping it.
func NewFileStoreIn(parentDir, fileName string) (*FileStore, error) {
	return NewFileStoreWithOptions(FileStoreOptions{
		ParentDir: parentDir,
		FileName:  fileName,
	})
}

// NewFileStoreWithOptions creates a FileStore from an explicit path or from a
// constrained parent-directory/name pair. Path and ParentDir cannot be mixed
// with a separate FileName, and no default or environment-derived path is
// accepted.
func NewFileStoreWithOptions(options FileStoreOptions) (*FileStore, error) {
	return newFileStoreWithFS(options, defaultFileStoreFS{})
}

func newFileStoreWithFS(options FileStoreOptions, fileSystem fileStoreFS) (*FileStore, error) {
	if fileSystem == nil {
		return nil, fmt.Errorf("%w: filesystem is unavailable", ErrFileStoreIO)
	}

	var path string
	switch {
	case options.Path != "":
		if options.FileName != "" {
			return nil, fmt.Errorf("%w: ambiguous path", ErrFileStorePath)
		}
		var err error
		path, err = normalizeExplicitFilePath(options.Path)
		if err != nil {
			return nil, err
		}
		if options.ParentDir != "" {
			parent, err := normalizeExplicitDirectoryPath(options.ParentDir)
			if err != nil {
				return nil, err
			}
			if !sameFileStorePath(filepath.Dir(path), parent) {
				return nil, fmt.Errorf("%w: path is outside parent", ErrFileStorePath)
			}
		}
	case options.ParentDir != "":
		if options.FileName == "" {
			return nil, fmt.Errorf("%w: file name is required", ErrFileStorePath)
		}
		parent, err := normalizeExplicitDirectoryPath(options.ParentDir)
		if err != nil {
			return nil, err
		}
		if err := validateChildFileName(options.FileName); err != nil {
			return nil, err
		}
		path = filepath.Join(parent, options.FileName)
	default:
		return nil, fmt.Errorf("%w: explicit path is required", ErrFileStorePath)
	}

	resourceKeys := []string{fileStoreLockKey(path), fileStoreLockKey(path + ".bak")}
	sort.Strings(resourceKeys)
	resourceKeys = uniqueStrings(resourceKeys)
	locks := make([]*fileStorePathLock, len(resourceKeys))
	fileStoreLocksMu.Lock()
	for i, key := range resourceKeys {
		lock := fileStoreLocks[key]
		if lock == nil {
			lock = &fileStorePathLock{}
			fileStoreLocks[key] = lock
		}
		locks[i] = lock
	}
	fileStoreLocksMu.Unlock()

	return &FileStore{
		path:         path,
		backupPath:   path + ".bak",
		createParent: options.CreateParent,
		fs:           fileSystem,
		locks:        locks,
	}, nil
}

// Path returns the explicitly configured path. It is intended for local
// management UI/CLI display; filesystem errors never include this value.
func (s *FileStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// BackupPath returns the one sibling backup managed by this store.
func (s *FileStore) BackupPath() string {
	if s == nil {
		return ""
	}
	return s.backupPath
}

// FileStoreCapability describes the intentionally limited trust boundary of
// this persistence helper. ProductionReady is permanently false until the
// caller supplies the missing OS-level hardening and cross-process locking.
type FileStoreCapability struct {
	ProductionReady bool
}

// Capability reports whether this store may be connected to a production
// authorization/runtime adapter. It is always false for FileStore.
func (s *FileStore) Capability() FileStoreCapability {
	return FileStoreCapability{ProductionReady: false}
}

// ProductionReady is an explicit hard gate for future adapters. Keeping this
// method on the convenience type makes accidental production wiring visible
// at the call site instead of being inferred from successful local tests.
func (s *FileStore) ProductionReady() bool { return false }

// RequireProductionReady always fails for this implementation. A future
// production store must provide a different implementation with Windows
// handle/ACL/reparse defenses and an OS-level inter-process lock.
func (s *FileStore) RequireProductionReady() error { return ErrUnsafeForProduction }

// Load reads and validates the current configuration. The returned revision
// is a SHA-256 digest of canonical JSON, prefixed with "sha256:". It is
// stable across harmless formatting changes and can be supplied to a CAS
// operation.
func (s *FileStore) Load() (Snapshot, error) {
	if s == nil || s.fs == nil || len(s.locks) == 0 {
		return Snapshot{}, fmt.Errorf("%w: store is unavailable", ErrFileStoreIO)
	}
	release := s.lockResources(false)
	defer release()

	data, exists, err := s.readPath(s.path)
	if err != nil {
		return Snapshot{}, err
	}
	if !exists {
		return Snapshot{}, ErrConfigNotFound
	}
	return snapshotFromData(data)
}

// Save atomically persists next, retaining the previous bytes in the store's
// sibling .bak file when a previous configuration exists. It does not use a
// revision check; callers that need lost-update protection should use
// SaveIfRevision.
func (s *FileStore) Save(next Config) (Snapshot, error) {
	return s.save(next, "", false)
}

// SaveIfRevision atomically persists next only when expected matches the
// current canonical configuration revision. An empty expected revision is
// valid only when the configuration file does not yet exist.
func (s *FileStore) SaveIfRevision(expected string, next Config) (Snapshot, error) {
	// Empty is the explicit create-if-absent CAS value. It must not silently
	// mean unconditional replacement.
	return s.save(next, expected, true)
}

func (s *FileStore) save(next Config, expected string, checkRevision bool) (Snapshot, error) {
	if s == nil || s.fs == nil || len(s.locks) == 0 {
		return Snapshot{}, fmt.Errorf("%w: store is unavailable", ErrFileStoreIO)
	}
	if err := next.validate(); err != nil {
		return Snapshot{}, err
	}
	data, err := json.Marshal(next)
	if err != nil {
		return Snapshot{}, fmt.Errorf("%w: configuration encoding failed", ErrInvalid)
	}
	if len(data) > MaxConfigBytes {
		return Snapshot{}, fmt.Errorf("%w: configuration exceeds size limit", ErrInvalid)
	}
	canonical, err := Parse(data)
	if err != nil {
		return Snapshot{}, fmt.Errorf("%w: configuration encoding failed", ErrInvalid)
	}
	revision, err := revisionForConfig(canonical)
	if err != nil {
		return Snapshot{}, err
	}

	release := s.lockResources(true)
	defer release()
	if err := s.ensureParent(); err != nil {
		return Snapshot{}, err
	}
	current, exists, err := s.readPath(s.path)
	if err != nil {
		return Snapshot{}, err
	}
	if checkRevision {
		if err := checkStoredRevision(current, exists, expected); err != nil {
			return Snapshot{}, err
		}
	}
	if err := s.atomicPersist(current, exists, data); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{config: canonical.Clone(), revision: revision}, nil
}

// RestoreBackup explicitly restores the store-managed sibling backup. The
// backup is parsed before any write; restoring a malformed or oversized
// backup leaves the current file untouched.
func (s *FileStore) RestoreBackup() (Snapshot, error) {
	return s.restoreBackup("", false)
}

// RestoreBackupIfRevision restores the sibling backup only if expected
// matches the current configuration revision. It is the recovery equivalent
// of SaveIfRevision.
func (s *FileStore) RestoreBackupIfRevision(expected string) (Snapshot, error) {
	if expected == "" {
		return Snapshot{}, fmt.Errorf("%w: expected revision required", ErrRevisionConflict)
	}
	return s.restoreBackup(expected, true)
}

func (s *FileStore) restoreBackup(expected string, checkRevision bool) (Snapshot, error) {
	if s == nil || s.fs == nil || len(s.locks) == 0 {
		return Snapshot{}, fmt.Errorf("%w: store is unavailable", ErrFileStoreIO)
	}
	release := s.lockResources(true)
	defer release()
	if err := s.ensureParent(); err != nil {
		return Snapshot{}, err
	}
	backup, exists, err := s.readPath(s.backupPath)
	if err != nil {
		return Snapshot{}, err
	}
	if !exists {
		return Snapshot{}, ErrBackupNotFound
	}
	backupConfig, err := parseStoredConfig(backup)
	if err != nil {
		return Snapshot{}, err
	}
	if checkRevision {
		current, currentExists, err := s.readPath(s.path)
		if err != nil {
			return Snapshot{}, err
		}
		if err := checkStoredRevision(current, currentExists, expected); err != nil {
			return Snapshot{}, err
		}
	}
	if err := s.restorePersist(backup); err != nil {
		return Snapshot{}, err
	}
	revision, err := revisionForConfig(backupConfig)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{config: backupConfig.Clone(), revision: revision}, nil
}

func (s *FileStore) ensureParent() error {
	parent := filepath.Dir(s.path)
	info, err := s.fs.Lstat(parent)
	if errors.Is(err, fs.ErrNotExist) {
		if !s.createParent {
			return ErrConfigNotFound
		}
		if err := s.fs.MkdirAll(parent, 0700); err != nil {
			return fileStoreIO("create parent", err)
		}
		info, err = s.fs.Lstat(parent)
	}
	if err != nil {
		return fileStoreIO("inspect parent", err)
	}
	if info == nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%w: parent is not a regular directory", ErrFileStorePath)
	}
	return nil
}

func (s *FileStore) readPath(name string) ([]byte, bool, error) {
	info, err := s.fs.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fileStoreIO("inspect file", err)
	}
	if info == nil || !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 {
		return nil, false, fmt.Errorf("%w: destination must be a regular file", ErrFileStorePath)
	}
	file, err := s.fs.OpenFile(name, os.O_RDONLY, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fileStoreIO("open file", err)
	}
	data, readErr := io.ReadAll(io.LimitReader(file, MaxConfigBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return nil, false, fileStoreIO("read file", firstError(readErr, closeErr))
	}
	if len(data) > MaxConfigBytes {
		return nil, false, fmt.Errorf("%w: configuration exceeds size limit", ErrInvalid)
	}
	return data, true, nil
}

func (s *FileStore) atomicPersist(previous []byte, previousExists bool, data []byte) error {
	if previousExists {
		if err := s.writeTempAndReplace(s.backupPath, previous, "backup"); err != nil {
			return err
		}
	}
	return s.writeTempAndReplace(s.path, data, "config")
}

// restorePersist leaves the recovery source unchanged and replaces only the
// main file. This intentionally is not a swap: keeping .bak immutable means a
// replacement or directory-sync failure never destroys the bytes needed to
// retry recovery. A later successful Save will rotate the then-current value
// into .bak through the normal save path.
func (s *FileStore) restorePersist(backup []byte) error {
	return s.writeTempAndReplace(s.path, backup, "restore-config")
}

func (s *FileStore) writeTempAndReplace(destination string, data []byte, kind string) error {
	var file fileStoreFile
	var temp string
	var err error
	for attempt := 0; attempt < 16; attempt++ {
		var pathErr error
		temp, pathErr = temporaryPath(destination, kind)
		if pathErr != nil {
			return fileStoreIO("name temporary file", pathErr)
		}
		file, err = s.fs.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrExist) {
			return fileStoreIO("create temporary file", err)
		}
	}
	if file == nil {
		return fileStoreIO("create temporary file", err)
	}
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = s.fs.Remove(temp)
		}
	}()

	if err := writeAll(file, data); err != nil {
		_ = file.Close()
		return fileStoreIO("write temporary file", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fileStoreIO("sync temporary file", err)
	}
	if err := file.Chmod(0600); err != nil {
		_ = file.Close()
		return fileStoreIO("restrict temporary file", err)
	}
	if err := file.Close(); err != nil {
		return fileStoreIO("close temporary file", err)
	}

	info, err := s.fs.Lstat(destination)
	if err == nil {
		if info == nil || !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%w: destination must be a regular file", ErrFileStorePath)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fileStoreIO("inspect destination", err)
	}
	if err := s.fs.Replace(temp, destination); err != nil {
		return fileStoreIO("replace configuration", err)
	}
	removeTemp = false
	if err := s.fs.SyncDir(filepath.Dir(destination)); err != nil {
		return fileStoreIO("sync configuration directory", err)
	}
	return nil
}

func checkStoredRevision(data []byte, exists bool, expected string) error {
	if !exists {
		if expected == "" {
			return nil
		}
		return ErrRevisionConflict
	}
	if expected == "" {
		return ErrRevisionConflict
	}
	revision, err := revisionForData(data)
	if err != nil {
		return err
	}
	if revision != expected {
		return ErrRevisionConflict
	}
	return nil
}

func snapshotFromData(data []byte) (Snapshot, error) {
	c, err := parseStoredConfig(data)
	if err != nil {
		return Snapshot{}, err
	}
	revision, err := revisionForConfig(c)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{config: c.Clone(), revision: revision}, nil
}

func parseStoredConfig(data []byte) (Config, error) {
	c, err := Parse(data)
	if err != nil {
		return Config{}, fmt.Errorf("%w: stored configuration is invalid", ErrInvalid)
	}
	return c, nil
}

func revisionForData(data []byte) (string, error) {
	c, err := parseStoredConfig(data)
	if err != nil {
		return "", err
	}
	return revisionForConfig(c)
}

func revisionForConfig(c Config) (string, error) {
	data, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("%w: configuration encoding failed", ErrInvalid)
	}
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func writeAll(file fileStoreFile, data []byte) error {
	for len(data) > 0 {
		written, err := file.Write(data)
		if err != nil {
			return err
		}
		if written <= 0 || written > len(data) {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func temporaryPath(destination, kind string) (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	base := filepath.Base(destination)
	return filepath.Join(filepath.Dir(destination), "."+base+"."+kind+"-"+hex.EncodeToString(random[:])), nil
}

func normalizeExplicitFilePath(value string) (string, error) {
	if !validExplicitPath(value) || !filepath.IsAbs(value) {
		return "", fmt.Errorf("%w: absolute path is required", ErrFileStorePath)
	}
	clean := filepath.Clean(value)
	if clean == "." || clean == string(filepath.Separator) || filepath.VolumeName(clean) != "" && clean == filepath.VolumeName(clean)+string(filepath.Separator) {
		return "", fmt.Errorf("%w: file path is not a regular file location", ErrFileStorePath)
	}
	return clean, nil
}

func normalizeExplicitDirectoryPath(value string) (string, error) {
	if !validExplicitPath(value) || !filepath.IsAbs(value) {
		return "", fmt.Errorf("%w: absolute parent directory is required", ErrFileStorePath)
	}
	clean := filepath.Clean(value)
	if clean == "." {
		return "", fmt.Errorf("%w: invalid parent directory", ErrFileStorePath)
	}
	return clean, nil
}

func validExplicitPath(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func validateChildFileName(value string) error {
	if !validExplicitPath(value) || filepath.IsAbs(value) || filepath.Base(value) != value || value == "." || value == ".." || strings.ContainsAny(value, `/\\:`) {
		return fmt.Errorf("%w: file name must be one direct child", ErrFileStorePath)
	}
	return nil
}

func fileStoreLockKey(path string) string {
	if runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}

func sameFileStorePath(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func fileStoreIO(operation string, _ error) error {
	return fmt.Errorf("%w: %s", ErrFileStoreIO, operation)
}

func firstError(first, second error) error {
	if first != nil {
		return first
	}
	return second
}

// fileStoreFile is deliberately smaller than *os.File so crash points can be
// tested without deleting or corrupting a real configuration.
type fileStoreFile interface {
	io.Reader
	io.Writer
	Sync() error
	Chmod(fs.FileMode) error
	Close() error
}

type fileStoreFS interface {
	OpenFile(name string, flag int, perm fs.FileMode) (fileStoreFile, error)
	Lstat(name string) (fs.FileInfo, error)
	MkdirAll(path string, perm fs.FileMode) error
	Remove(name string) error
	Replace(oldPath, newPath string) error
	SyncDir(path string) error
}

type defaultFileStoreFS struct{}

func (defaultFileStoreFS) OpenFile(name string, flag int, perm fs.FileMode) (fileStoreFile, error) {
	return os.OpenFile(name, flag, perm)
}

func (defaultFileStoreFS) Lstat(name string) (fs.FileInfo, error) {
	return os.Lstat(name)
}

func (defaultFileStoreFS) MkdirAll(path string, perm fs.FileMode) error {
	return os.MkdirAll(path, perm)
}

func (defaultFileStoreFS) Remove(name string) error {
	return os.Remove(name)
}

func (defaultFileStoreFS) Replace(oldPath, newPath string) error {
	return replaceFile(oldPath, newPath)
}

func (defaultFileStoreFS) SyncDir(path string) error {
	// Windows has no portable directory fsync. The temporary file is flushed
	// before MoveFileEx; the platform helper requests write-through there.
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
