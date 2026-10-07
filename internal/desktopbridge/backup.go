package desktopbridge

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/LE-saber/Local-Probe/internal/config"
)

const BackupExtension = ".lpbackup"
const backupSchema = "local-probe.backup.v1"
const MaxBackupBytes = config.MaxConfigBytes + (64 << 10)

var ErrBackupCancelled = errors.New("backup dialog cancelled")
var errBackupInvalid = errors.New("invalid backup")

// AppendBackupExtension treats the chosen name as a base name. Do not rely
// on OPENFILENAME's legacy default-extension handling for this long suffix.
func AppendBackupExtension(path string) string {
	if !strings.EqualFold(filepath.Ext(path), BackupExtension) {
		return path + BackupExtension
	}
	return path
}

type backupDocument struct {
	Schema    string          `json:"schema_version"`
	CreatedAt time.Time       `json:"created_at"`
	Config    json.RawMessage `json:"config"`
	SHA256    string          `json:"config_sha256"`
}

type BackupSelection struct {
	ID          string    `json:"selection_id"`
	Revision    string    `json:"expected_revision"`
	CreatedAt   time.Time `json:"created_at"`
	Connections int       `json:"connections"`
	Profiles    int       `json:"profiles"`
	Roots       int       `json:"roots"`
}
type backupCandidate struct {
	id, revision string
	expires      time.Time
	config       config.Config
}

// The checksum detects corruption, not provenance or authenticity. A backup
// is untrusted input and must still pass Config validation and restore gates.
func EncodeBackup(cfg config.Config, created time.Time) ([]byte, error) {
	if created.IsZero() {
		return nil, errBackupInvalid
	}
	data, err := cfg.MarshalJSON()
	if err != nil || len(data) > config.MaxConfigBytes {
		return nil, errBackupInvalid
	}
	if _, err := config.Parse(data); err != nil {
		return nil, errBackupInvalid
	}
	digest := sha256.Sum256(data)
	document, err := json.Marshal(backupDocument{backupSchema, created.UTC(), data, hex.EncodeToString(digest[:])})
	if err != nil || len(document) > MaxBackupBytes {
		return nil, errBackupInvalid
	}
	return document, nil
}

func decodeBackup(data []byte) (config.Config, time.Time, error) {
	invalid := func() (config.Config, time.Time, error) { return config.Config{}, time.Time{}, errBackupInvalid }
	if len(data) == 0 || len(data) > MaxBackupBytes || !utf8.Valid(data) || !json.Valid(data) {
		return invalid()
	}
	// Reject duplicate object keys (also inside Config), not just unknown fields.
	if err := uniqueJSONKeys(json.NewDecoder(bytes.NewReader(data)), 0); err != nil {
		return invalid()
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var doc backupDocument
	if decoder.Decode(&doc) != nil || ensureEOF(decoder) != nil || doc.Schema != backupSchema || doc.CreatedAt.IsZero() || len(doc.Config) > config.MaxConfigBytes {
		return invalid()
	}
	cfg, err := config.Parse(doc.Config)
	if err != nil {
		return invalid()
	}
	canonical, err := cfg.MarshalJSON()
	if err != nil {
		return invalid()
	}
	digest := sha256.Sum256(canonical)
	if doc.SHA256 != hex.EncodeToString(digest[:]) {
		return invalid()
	}
	return cfg, doc.CreatedAt, nil
}

func uniqueJSONKeys(decoder *json.Decoder, depth int) error {
	if depth > 64 {
		return errBackupInvalid
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return errBackupInvalid
			}
			seen[name] = true
			if err := uniqueJSONKeys(decoder, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := uniqueJSONKeys(decoder, depth+1); err != nil {
				return err
			}
		}
	default:
		return errBackupInvalid
	}
	_, err = decoder.Token()
	return err
}

func (s *Service) pickBackup() (Snapshot, *rpcError) {
	s.mu.Lock()
	if s.closed || s.busy || s.nativeBusy {
		closed := s.closed
		s.mu.Unlock()
		if closed {
			return s.snapshot(), errorFor("closed")
		}
		return s.snapshot(), errorFor("busy")
	}
	s.backupCandidate = nil
	if s.options.LoadBackup == nil {
		s.mu.Unlock()
		return s.snapshot(), errorFor("backup_unavailable")
	}
	s.nativeBusy = true
	loader := s.options.LoadBackup
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.nativeBusy = false; s.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	data, err := loader(ctx)
	if errors.Is(err, ErrBackupCancelled) {
		return s.snapshot(), nil
	}
	if errors.Is(err, errBackupInvalid) {
		return s.snapshot(), errorFor("backup_invalid")
	}
	if err != nil || ctx.Err() != nil {
		return s.snapshot(), errorFor("backup_read_failed")
	}
	cfg, created, err := decodeBackup(data)
	if err != nil {
		return s.snapshot(), errorFor("backup_invalid")
	}
	current, err := s.store.Load()
	revision := ""
	if err == nil {
		revision = current.Revision()
	} else if !errors.Is(err, config.ErrConfigNotFound) {
		return s.snapshot(), errorFor("config_invalid")
	}
	var random [24]byte
	if _, err := rand.Read(random[:]); err != nil {
		return s.snapshot(), errorFor("internal_error")
	}
	id := hex.EncodeToString(random[:])
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return s.snapshot(), errorFor("closed")
	}
	s.backupCandidate = &backupCandidate{id, revision, time.Now().Add(5 * time.Minute), cfg}
	s.mu.Unlock()
	result := s.snapshot()
	result.BackupSelection = &BackupSelection{id, revision, created, len(cfg.Connections()), len(cfg.Profiles()), len(cfg.Roots())}
	return result, nil
}

// Filesystem helpers are only called with native-dialog paths, never RPC
// paths. They use exclusive creation, bounded regular-file reads and os.Root
// confinement. They are private desktop conveniences, not an ACL/hardlink or
// cross-process filesystem security boundary.
func openBackupParent(path string) (*os.Root, string, error) {
	if !filepath.IsAbs(path) || !strings.EqualFold(filepath.Ext(path), BackupExtension) || strings.ContainsAny(path, "\x00\r\n") {
		return nil, "", errBackupInvalid
	}
	name := filepath.Base(path)
	if strings.Contains(name, ":") {
		return nil, "", errBackupInvalid
	}
	parent, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, "", errBackupInvalid
	}
	return parent, name, nil
}

func ReadBackupFile(path string) ([]byte, error) {
	parent, name, err := openBackupParent(path)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	info, err := parent.Lstat(name)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errBackupInvalid
	}
	file, err := parent.Open(name)
	if err != nil {
		return nil, errBackupInvalid
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) || opened.Size() > MaxBackupBytes {
		return nil, errBackupInvalid
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxBackupBytes+1))
	if err != nil || len(data) > MaxBackupBytes {
		return nil, errBackupInvalid
	}
	if _, _, err := decodeBackup(data); err != nil {
		return nil, err
	}
	return data, nil
}

func SaveBackupFile(path string, data []byte) error {
	if _, _, err := decodeBackup(data); err != nil {
		return err
	}
	parent, name, err := openBackupParent(path)
	if err != nil {
		return err
	}
	defer parent.Close()
	file, err := parent.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errBackupInvalid
	}
	success := false
	defer func() {
		_ = file.Close()
		if !success {
			_ = parent.Remove(name)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return errBackupInvalid
	}
	if file.Sync() != nil || file.Close() != nil {
		return errBackupInvalid
	}
	success = true
	return nil
}
