package previewui

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/LE-saber/Local-Probe/internal/audit"
)

var (
	ErrExportExists = errors.New("preview export destination already exists")
	ErrExportPath   = errors.New("invalid preview export destination")
	ErrExportUnsafe = errors.New("preview export contains unsafe data")
)

// ValidateAndScrubExport applies a second, UI-boundary scrub to an adapter
// payload.  Only keys used by the fixed Preview/support document are accepted;
// unknown future fields fail closed instead of silently becoming a data leak.
func ValidateAndScrubExport(data []byte) ([]byte, error) {
	if len(data) == 0 || len(data) > MaxExportBytes {
		return nil, ErrExportLimit
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, ErrInvalidModel
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, ErrInvalidModel
	}
	if err := scrubExportValue(value, ""); err != nil {
		return nil, err
	}
	clean, err := json.Marshal(value)
	if err != nil {
		return nil, ErrInvalidModel
	}
	if len(clean) > MaxExportBytes {
		return nil, ErrExportLimit
	}
	return clean, nil
}

var safeExportKeys = map[string]struct{}{
	"schema_version": {}, "generated_at": {}, "production_ready": {}, "unconfigured": {}, "status": {}, "error_code": {},
	"overview": {}, "connections": {}, "developer_rules": {}, "audit": {}, "logs": {}, "diagnostics": {}, "management_actions": {}, "omitted": {},
	"config_revision": {}, "root_count": {}, "profile_count": {}, "connection_count": {}, "enabled_connection_count": {}, "status_known": {}, "ready_connections": {}, "degraded_connections": {}, "truncated": {},
	"connection_id": {}, "label": {}, "label_omitted": {}, "profile_id": {}, "enabled": {}, "transport": {}, "tunnel_configured": {}, "state": {}, "attempt": {}, "last_error": {},
	"available": {}, "allowed_connection_ids": {}, "rules": {}, "id": {}, "kind": {}, "variant_ids": {}, "slot_kinds": {}, "reason": {},
	"records": {}, "dropped": {}, "corrupt": {},
	"timestamp": {}, "component": {}, "event_type": {}, "severity": {}, "outcome": {}, "duration_ms": {},
	"build": {}, "version": {}, "goos": {}, "goarch": {}, "health": {}, "omissions": {}, "corrupt_records": {},
}

func scrubExportValue(value any, key string) error {
	if key != "" {
		if _, ok := safeExportKeys[key]; !ok {
			return ErrExportUnsafe
		}
	}
	switch typed := value.(type) {
	case map[string]any:
		if len(typed) > 128 {
			return ErrExportLimit
		}
		for name, child := range typed {
			if len(name) > 128 || name != strings.ToLower(name) {
				return ErrExportUnsafe
			}
			if err := scrubExportValue(child, name); err != nil {
				return err
			}
		}
	case []any:
		if len(typed) > MaxItems {
			return ErrExportLimit
		}
		for _, child := range typed {
			if err := scrubExportValue(child, key); err != nil {
				return err
			}
		}
	case string:
		if len(typed) > MaxTextBytes || audit.Redact(typed) != typed {
			return ErrExportUnsafe
		}
		if !validExportString(key, typed) {
			return ErrExportUnsafe
		}
	case json.Number:
		if len(typed.String()) > 32 || !validExportNumber(typed.String()) {
			return ErrExportUnsafe
		}
	case bool, nil:
		return nil
	default:
		return ErrExportUnsafe
	}
	return nil
}

func validExportString(key, value string) bool {
	switch key {
	case "generated_at", "timestamp":
		_, err := time.Parse(time.RFC3339Nano, value)
		return err == nil
	case "label":
		return sanitizeLabel(value) != ""
	case "config_revision":
		return sanitizeRevision(value) != ""
	case "connection_id", "profile_id", "id", "variant_ids", "allowed_connection_ids":
		return sanitizeIdentifier(value) != "omitted"
	case "transport":
		return sanitizeTransport(value) != "unknown"
	case "schema_version", "status", "error_code", "component", "event_type", "severity", "outcome", "state", "last_error", "kind", "slot_kinds", "reason", "version", "build", "goos", "goarch", "omitted":
		return sanitizeAtom(value, MaxTextBytes) != ""
	default:
		// The key allowlist above is intentionally the primary boundary. Any
		// future string field must be reviewed here before the UI can export it.
		return false
	}
}

func validExportNumber(value string) bool {
	if value == "" || strings.ContainsAny(value, ".eE") {
		return false
	}
	if strings.HasPrefix(value, "-") {
		return false
	}
	_, err := strconv.ParseUint(value, 10, 64)
	return err == nil
}

// SaveExport writes one already-scrubbed JSON document.  The destination must
// be a new regular file in an existing regular directory; this keeps an
// accidental overwrite or symlink/reparse target out of the Preview path.
func SaveExport(path string, data []byte) error {
	clean, err := ValidateAndScrubExport(data)
	if err != nil {
		return err
	}
	if path == "" || filepath.Ext(path) != ".json" || strings.ContainsRune(path, 0) {
		return ErrExportPath
	}
	dir := filepath.Dir(path)
	dirInfo, err := os.Lstat(dir)
	if err != nil || dirInfo.Mode()&os.ModeSymlink != 0 || !dirInfo.IsDir() {
		return ErrExportPath
	}
	if info, statErr := os.Lstat(path); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return ErrExportPath
		}
		return ErrExportExists
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return ErrExportPath
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrExportExists
		}
		return ErrExportPath
	}
	remove := true
	defer func() {
		if remove {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(clean); err != nil {
		_ = file.Close()
		return ErrExportPath
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return ErrExportPath
	}
	if err := file.Close(); err != nil {
		return ErrExportPath
	}
	remove = false
	return nil
}
