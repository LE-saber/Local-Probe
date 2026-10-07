// Package supportbundle builds a small, local-only diagnostic bundle for the
// desktop preview.  It deliberately accepts already-sanitized projections;
// it never reads logs, resolves credentials, contacts a network, or invokes a
// process.
package supportbundle

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/LE-saber/Local-Probe/internal/auditreader"
	"github.com/LE-saber/Local-Probe/internal/desktopadmin"
)

const (
	SchemaVersion   = "local-probe.support-bundle.v1"
	ProductionReady = false
	MaxBytes        = 64 << 10
	maxFieldBytes   = 128
	maxPathBytes    = 4096
)

var (
	ErrInvalidInput  = errors.New("invalid support bundle input")
	ErrSensitiveData = errors.New("sensitive support bundle data rejected")
	ErrSizeLimit     = errors.New("support bundle exceeds size limit")
	ErrInvalidPath   = errors.New("invalid support bundle directory")
	ErrTargetExists  = errors.New("support bundle destination already exists")
	ErrWrite         = errors.New("support bundle write failed")
)

// BuildInfo is the only build metadata admitted to a bundle.  Values are
// bounded atoms; callers must not put paths, URLs, credential references, or
// arbitrary diagnostic messages here.
type BuildInfo struct {
	Version   string `json:"version,omitempty"`
	Commit    string `json:"commit,omitempty"`
	GoVersion string `json:"go_version,omitempty"`
	OS        string `json:"os,omitempty"`
	Arch      string `json:"arch,omitempty"`
}

// Input contains only pre-sanitized desktop projections and an audit summary.
// It intentionally has no raw diagnostic entries, log text, root, tunnel,
// credential, command, or file fields.
type Input struct {
	Build          BuildInfo                         `json:"build"`
	Overview       desktopadmin.Overview             `json:"overview"`
	Statuses       desktopadmin.ConnectionStatusList `json:"statuses"`
	DeveloperRules desktopadmin.DeveloperRules       `json:"developer_rules"`
	Audit          auditreader.Summary               `json:"audit"`
}

// Bundle is the fixed wire shape emitted by Generate and Write.
type Bundle struct {
	SchemaVersion   string                            `json:"schema_version"`
	ProductionReady bool                              `json:"production_ready"`
	Build           BuildInfo                         `json:"build"`
	Overview        desktopadmin.Overview             `json:"overview"`
	Statuses        desktopadmin.ConnectionStatusList `json:"statuses"`
	DeveloperRules  desktopadmin.DeveloperRules       `json:"developer_rules"`
	Audit           auditreader.Summary               `json:"audit"`
}

// Generate validates the input, applies the final sensitive-data/path scan,
// and returns a bounded JSON document.  The returned bytes contain no source
// file paths or raw log records.
func Generate(input Input) ([]byte, error) {
	if err := validateInput(input); err != nil {
		return nil, err
	}
	bundle := Bundle{
		SchemaVersion: SchemaVersion, ProductionReady: ProductionReady,
		Build: cloneBuildInfo(input.Build), Overview: input.Overview,
		Statuses: cloneStatuses(input.Statuses), DeveloperRules: cloneDeveloperRules(input.DeveloperRules), Audit: input.Audit,
	}
	data, err := json.Marshal(bundle)
	if err != nil {
		return nil, ErrInvalidInput
	}
	if len(data) == 0 || len(data) > MaxBytes {
		return nil, ErrSizeLimit
	}
	if err := secondPass(data); err != nil {
		return nil, err
	}
	return data, nil
}

// Build is a concise alias for Generate.
func Build(input Input) ([]byte, error) { return Generate(input) }

// Write creates one new support bundle in directory and returns its path.
// The caller chooses the directory explicitly; the function never overwrites
// an existing file and uses a same-directory atomic no-replace operation.
func Write(directory string, input Input) (string, error) {
	clean, err := validateDirectory(directory)
	if err != nil {
		return "", err
	}
	data, err := Generate(input)
	if err != nil {
		return "", err
	}

	for attempt := 0; attempt < 8; attempt++ {
		name, err := randomBundleName()
		if err != nil {
			return "", ErrWrite
		}
		target := filepath.Join(clean, name)
		temp := target + ".tmp"
		if pathExists(target) || pathExists(temp) {
			continue
		}
		file, err := os.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			if pathExists(temp) || pathExists(target) {
				continue
			}
			return "", ErrWrite
		}
		ok := false
		func() {
			defer func() {
				if !ok {
					_ = file.Close()
					_ = os.Remove(temp)
				}
			}()
			written, err := file.Write(data)
			if err != nil || written != len(data) {
				return
			}
			if err := file.Sync(); err != nil {
				return
			}
			if err := file.Chmod(0600); err != nil {
				return
			}
			if err := file.Close(); err != nil {
				return
			}
			if err := tightenFilePermissions(temp); err != nil {
				return
			}
			if err := atomicNoReplace(temp, target); err != nil {
				return
			}
			ok = true
		}()
		if ok {
			return target, nil
		}
		if pathExists(target) {
			continue
		}
		return "", ErrWrite
	}
	return "", ErrTargetExists
}

// WriteFile is an alias retained for callers that name the operation after
// its file result.
func WriteFile(directory string, input Input) (string, error) { return Write(directory, input) }

func validateInput(input Input) error {
	for _, value := range []string{input.Build.Version, input.Build.Commit, input.Build.GoVersion, input.Build.OS, input.Build.Arch} {
		if containsSensitive(value) || looksLikePath(value) {
			return ErrSensitiveData
		}
	}
	if !safeBuildInfo(input.Build) {
		return ErrInvalidInput
	}
	if !validOverview(input.Overview) || !validStatuses(input.Statuses) || !validDeveloperRules(input.DeveloperRules) {
		return ErrInvalidInput
	}
	if err := input.Audit.Validate(); err != nil {
		return ErrInvalidInput
	}
	return nil
}

func safeBuildInfo(info BuildInfo) bool {
	for _, value := range []string{info.Version, info.Commit, info.GoVersion, info.OS, info.Arch} {
		if value != "" && !safeAtom(value, maxFieldBytes) {
			return false
		}
	}
	return true
}

func validOverview(value desktopadmin.Overview) bool {
	data, err := json.Marshal(value)
	return err == nil && len(data) <= desktopadmin.MaxWireBytes
}

func validStatuses(value desktopadmin.ConnectionStatusList) bool {
	data, err := json.Marshal(value)
	return err == nil && len(data) <= desktopadmin.MaxWireBytes
}

func validDeveloperRules(value desktopadmin.DeveloperRules) bool {
	data, err := json.Marshal(value)
	return err == nil && len(data) <= desktopadmin.MaxWireBytes
}

func cloneBuildInfo(value BuildInfo) BuildInfo { return value }

func cloneStatuses(value desktopadmin.ConnectionStatusList) desktopadmin.ConnectionStatusList {
	value.Entries = append([]desktopadmin.ConnectionStatus(nil), value.Entries...)
	return value
}

func cloneDeveloperRules(value desktopadmin.DeveloperRules) desktopadmin.DeveloperRules {
	value.AllowedConnectionIDs = append([]string(nil), value.AllowedConnectionIDs...)
	value.Rules = append([]desktopadmin.DeveloperRule(nil), value.Rules...)
	for index := range value.Rules {
		value.Rules[index].VariantIDs = append([]string(nil), value.Rules[index].VariantIDs...)
		value.Rules[index].SlotKinds = append(value.Rules[index].SlotKinds[:0:0], value.Rules[index].SlotKinds...)
	}
	return value
}

func secondPass(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return ErrInvalidInput
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return ErrInvalidInput
	}
	if err := inspectValue(value); err != nil {
		return err
	}
	return nil
}

func inspectValue(value any) error {
	switch typed := value.(type) {
	case string:
		if containsSensitive(typed) || looksLikePath(typed) || !utf8.ValidString(typed) {
			return ErrSensitiveData
		}
	case []any:
		for _, child := range typed {
			if err := inspectValue(child); err != nil {
				return err
			}
		}
	case map[string]any:
		for key, child := range typed {
			if containsSensitive(key) || looksLikePath(key) {
				return ErrSensitiveData
			}
			if err := inspectValue(child); err != nil {
				return err
			}
		}
	}
	return nil
}

func safeAtom(value string, max int) bool {
	if value == "" || len(value) > max || !utf8.ValidString(value) || containsSensitive(value) || looksLikePath(value) {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r > 0x7f || r == ' ' || r == '\t' || r == '\r' || r == '\n' {
			return false
		}
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._:+/-", r)) {
			return false
		}
	}
	return true
}

func containsSensitive(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{"token", "secret", "password", "passwd", "api_key", "apikey", "authorization", "bearer", "cookie", "private_key", "credential", "client_secret"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func looksLikePath(value string) bool {
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\\`) || strings.HasPrefix(strings.ToLower(value), "file:") || strings.Contains(value, "://") {
		return true
	}
	return len(value) >= 3 && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) && value[1] == ':' && (value[2] == '\\' || value[2] == '/')
}

func randomBundleName() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return "local-probe-support-" + hex.EncodeToString(random[:]) + ".json", nil
}

func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
