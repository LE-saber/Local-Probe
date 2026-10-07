package supportbundle

// This file owns the export boundary used by the native Preview UI.  The
// older Generate/Write API above is intentionally kept for callers that
// already build the compact supportbundle.Input projection.  GenerateDocument
// and WriteDocument accept a JSON-shaped, already safe UI projection so the
// UI does not need to duplicate the size, schema, redaction, or file-write
// policy.

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	PreviewSchemaVersion = "local-probe.preview.v1"
	previewMaxItems      = 128
	previewMaxTextBytes  = 512
)

var previewDocumentKeys = map[string]struct{}{
	"schema_version": {}, "generated_at": {}, "production_ready": {}, "unconfigured": {}, "status": {}, "error_code": {},
	"overview": {}, "connections": {}, "developer_rules": {}, "audit": {}, "logs": {}, "diagnostics": {}, "management_actions": {}, "omitted": {},
	"config_revision": {}, "root_count": {}, "profile_count": {}, "connection_count": {}, "enabled_connection_count": {}, "status_known": {}, "ready_connections": {}, "degraded_connections": {}, "truncated": {},
	"connection_id": {}, "label": {}, "label_omitted": {}, "profile_id": {}, "enabled": {}, "transport": {}, "tunnel_configured": {}, "state": {}, "attempt": {}, "last_error": {},
	"available": {}, "allowed_connection_ids": {}, "rules": {}, "id": {}, "kind": {}, "variant_ids": {}, "slot_kinds": {}, "reason": {},
	"records": {}, "dropped": {}, "corrupt": {},
	"timestamp": {}, "component": {}, "event_type": {}, "severity": {}, "outcome": {}, "duration_ms": {},
}

var previewDocumentRequiredRootKeys = map[string]struct{}{
	"schema_version": {}, "generated_at": {}, "production_ready": {}, "unconfigured": {}, "status": {},
	"overview": {}, "connections": {}, "developer_rules": {}, "audit": {}, "logs": {}, "diagnostics": {}, "management_actions": {},
}

// GenerateDocument marshals one Preview snapshot and applies the final
// fixed-schema, 64 KiB, path/sensitive-data validation pass.  The argument is
// intentionally any so this package does not import the previewui package and
// create an import cycle; callers should pass the sanitized Preview Snapshot
// value, not raw configuration or audit data.
func GenerateDocument(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, ErrInvalidInput
	}
	return ValidateDocument(data)
}

// BuildDocument is an alias useful to adapters that call the operation a
// build rather than a generation.
func BuildDocument(value any) ([]byte, error) { return GenerateDocument(value) }

// ValidateDocument performs the same boundary check used immediately before
// writing a document.  It returns compact canonical JSON so the size checked
// here is the size that will actually be written.
func ValidateDocument(data []byte) ([]byte, error) {
	if len(data) == 0 || len(data) > MaxBytes {
		return nil, ErrSizeLimit
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, ErrInvalidInput
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, ErrInvalidInput
	}
	root, ok := value.(map[string]any)
	if !ok {
		return nil, ErrInvalidInput
	}
	if err := validatePreviewDocumentRoot(root); err != nil {
		return nil, err
	}
	data, err := json.Marshal(root)
	if err != nil {
		return nil, ErrInvalidInput
	}
	if len(data) == 0 || len(data) > MaxBytes {
		return nil, ErrSizeLimit
	}
	return data, nil
}

// WriteDocument validates data again, stages it in the destination
// directory, tightens the file permissions, and atomically publishes it
// without replacing an existing file.  It is the file-writing half of the
// Preview support-bundle adapter; callers do not need a second SaveExport
// implementation in the GUI package.
func WriteDocument(path string, data []byte) error {
	clean, err := validateDocumentTarget(path)
	if err != nil {
		return err
	}
	cleanData, err := ValidateDocument(data)
	if err != nil {
		return err
	}
	for attempt := 0; attempt < 8; attempt++ {
		temp, tempErr := temporaryDocumentPath(clean)
		if tempErr != nil {
			return ErrWrite
		}
		if pathExists(clean) || pathExists(temp) {
			continue
		}
		file, openErr := os.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if openErr != nil {
			if pathExists(temp) || pathExists(clean) {
				continue
			}
			return ErrWrite
		}
		published := false
		func() {
			defer func() {
				if !published {
					_ = file.Close()
					_ = os.Remove(temp)
				}
			}()
			written, writeErr := file.Write(cleanData)
			if writeErr != nil || written != len(cleanData) {
				return
			}
			if syncErr := file.Sync(); syncErr != nil {
				return
			}
			if chmodErr := file.Chmod(0600); chmodErr != nil {
				return
			}
			if closeErr := file.Close(); closeErr != nil {
				return
			}
			if permissionErr := tightenFilePermissions(temp); permissionErr != nil {
				return
			}
			if moveErr := atomicNoReplace(temp, clean); moveErr != nil {
				return
			}
			published = true
		}()
		if published {
			return nil
		}
		if pathExists(clean) {
			return ErrTargetExists
		}
		return ErrWrite
	}
	return ErrTargetExists
}

// SaveDocument is a naming alias for UI adapters that model this operation
// after a Save action.
func SaveDocument(path string, data []byte) error { return WriteDocument(path, data) }

func validatePreviewDocumentRoot(root map[string]any) error {
	if len(root) > 32 {
		return ErrSizeLimit
	}
	for key := range previewDocumentRequiredRootKeys {
		if _, ok := root[key]; !ok {
			return ErrInvalidInput
		}
	}
	for key, value := range root {
		if _, ok := previewDocumentKeys[key]; !ok {
			return ErrInvalidInput
		}
		if err := validatePreviewValue(value, key); err != nil {
			return err
		}
	}
	if schema, ok := root["schema_version"].(string); !ok || schema != PreviewSchemaVersion {
		return ErrInvalidInput
	}
	return nil
}

func validatePreviewValue(value any, key string) error {
	switch typed := value.(type) {
	case map[string]any:
		if len(typed) > previewMaxItems {
			return ErrSizeLimit
		}
		for name, child := range typed {
			if len(name) == 0 || len(name) > previewMaxTextBytes || name != strings.ToLower(name) {
				return ErrInvalidInput
			}
			if _, ok := previewDocumentKeys[name]; !ok {
				return ErrInvalidInput
			}
			if containsSensitive(name) || looksLikePath(name) {
				return ErrSensitiveData
			}
			if err := validatePreviewValue(child, name); err != nil {
				return err
			}
		}
	case []any:
		if len(typed) > previewMaxItems {
			return ErrSizeLimit
		}
		for _, child := range typed {
			if err := validatePreviewValue(child, key); err != nil {
				return err
			}
		}
	case string:
		if len(typed) > previewMaxTextBytes || !utf8.ValidString(typed) {
			return ErrInvalidInput
		}
		// The omitted list intentionally names categories such as
		// "credentials" and "paths".  Those are policy labels, not values
		// being exported, so they are safe only in this one fixed field.
		if key != "omitted" && (containsSensitive(typed) || looksLikePath(typed)) {
			return ErrSensitiveData
		}
		if !validPreviewString(key, typed) {
			return ErrInvalidInput
		}
	case json.Number:
		if !validPreviewNumber(typed.String()) {
			return ErrInvalidInput
		}
	case bool, nil:
		return nil
	default:
		return ErrInvalidInput
	}
	return nil
}

func validPreviewString(key, value string) bool {
	switch key {
	case "generated_at", "timestamp":
		_, err := time.Parse(time.RFC3339Nano, value)
		return err == nil
	case "label":
		return value != "" && len(value) <= 128
	case "config_revision":
		return validPreviewRevision(value)
	case "connection_id", "profile_id", "id", "variant_ids", "allowed_connection_ids":
		return validPreviewIdentifier(value)
	case "transport":
		return value == "local" || value == "openai_runtime" || value == "cloudflare_named" || value == "unknown"
	case "schema_version":
		return value == PreviewSchemaVersion
	default:
		return safePreviewAtom(value)
	}
}

func validPreviewNumber(value string) bool {
	if value == "" || strings.ContainsAny(value, ".eE") || strings.HasPrefix(value, "-") {
		return false
	}
	_, err := strconv.ParseUint(value, 10, 64)
	return err == nil
}

func validPreviewIdentifier(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func validPreviewRevision(value string) bool {
	if value == "" {
		return false
	}
	if len(value) >= 2 && len(value) <= 21 && value[0] == 'r' {
		return validPreviewIdentifier(value)
	}
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, r := range value[len("sha256:"):] {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

func safePreviewAtom(value string) bool {
	if value == "" || len(value) > previewMaxTextBytes {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r > 0x7f || r == ' ' || r == '\t' || r == '\r' || r == '\n' {
			return false
		}
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._:-", r)) {
			return false
		}
	}
	return true
}

func validateDocumentTarget(path string) (string, error) {
	if path == "" || len(path) > maxPathBytes || strings.ContainsRune(path, 0) || !filepath.IsAbs(path) || !strings.EqualFold(filepath.Ext(path), ".json") {
		return "", ErrInvalidPath
	}
	clean := filepath.Clean(path)
	directory := filepath.Dir(clean)
	if _, err := validateDirectory(directory); err != nil {
		return "", err
	}
	if info, err := os.Lstat(clean); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || fileInfoReparse(info) {
			return "", ErrInvalidPath
		}
		return "", ErrTargetExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", ErrInvalidPath
	}
	return clean, nil
}

func temporaryDocumentPath(target string) (string, error) {
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return target + ".tmp-" + hex.EncodeToString(random[:]), nil
}
