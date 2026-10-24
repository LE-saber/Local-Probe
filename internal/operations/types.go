// Package operations owns explicitly approved trusted-host jobs. It never treats
// a remote boolean, a transport credential, or a tool annotation as approval.
package operations

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/LE-saber/Local-Probe/internal/policy"
)

var (
	ErrInvalid  = errors.New("invalid_operation")
	ErrDenied   = errors.New("operation_denied")
	ErrConflict = errors.New("operation_conflict")
	ErrStorage  = errors.New("operation_storage_unavailable")
	ErrCapacity = errors.New("operation_capacity")
)

const Version = "local-probe.operations.v1"
const MaxContent = 128 << 10

// Subject is supplied ONLY by an authenticated ingress, never JSON arguments.
type Subject struct{ Principal, Connection string }

type Request struct {
	Nonce          string   `json:"nonce"`
	Kind           string   `json:"kind"`
	ToolID         string   `json:"tool_id,omitempty"`
	RootID         string   `json:"root_id"`
	Directory      string   `json:"directory,omitempty"`
	Args           []string `json:"args,omitempty"`
	Script         string   `json:"script,omitempty"`
	Path           string   `json:"path,omitempty"`
	Content        string   `json:"content,omitempty"`
	Edits          []Edit   `json:"edits,omitempty"`
	TimeoutSeconds int      `json:"timeout_seconds,omitempty"`
}
type Edit struct {
	Old string `json:"old"`
	New string `json:"new"`
}

// Tool is a locally configured runner, not an executable path chosen by MCP.
type Tool struct {
	ID          string   `json:"id"`
	Executable  string   `json:"executable"`
	SHA256      string   `json:"sha256"`
	ScriptType  string   `json:"script_type,omitempty"` // python, node, powershell, cmd
	Environment []string `json:"environment,omitempty"`
}
type Catalog struct {
	Version string `json:"version"`
	Tools   []Tool `json:"tools"`
}

type Plan struct {
	Subject        Subject `json:"subject"`
	Request        Request `json:"request"`
	Epoch          string  `json:"epoch"`
	ExpectedFile   string  `json:"expected_file,omitempty"`
	Before         string  `json:"before,omitempty"`
	PatchedContent string  `json:"patched_content,omitempty"`
	ExecutableHash string  `json:"executable_hash,omitempty"`
}

type Summary struct {
	ID          string    `json:"id"`
	Digest      string    `json:"digest"`
	State       string    `json:"state"`
	Kind        string    `json:"kind"`
	RootID      string    `json:"root_id"`
	ToolID      string    `json:"tool_id,omitempty"`
	Created     time.Time `json:"created"`
	Updated     time.Time `json:"updated"`
	Expires     time.Time `json:"expires"`
	ExitCode    int64     `json:"exit_code"`
	ErrorCode   string    `json:"error_code,omitempty"`
	StdoutBytes int       `json:"stdout_bytes"`
	StderrBytes int       `json:"stderr_bytes"`
}
type Record struct {
	Version  string  `json:"version"`
	Summary  Summary `json:"summary"`
	Plan     Plan    `json:"plan"`
	Sequence int     `json:"sequence"`
	Stdout   []byte  `json:"stdout,omitempty"`
	Stderr   []byte  `json:"stderr,omitempty"`
}
type Decision struct {
	Version                 string    `json:"version"`
	ID                      string    `json:"id"`
	Digest                  string    `json:"digest"`
	Approved                bool      `json:"approved"`
	TrustedHostAcknowledged bool      `json:"trusted_host_acknowledged"`
	Expires                 time.Time `json:"expires"`
}
type Output struct {
	Data       []byte `json:"data_base64"`
	NextOffset int    `json:"next_offset"`
	Total      int    `json:"total"`
}

type Workspace interface {
	WithDirectory(policy.BoundScope, string, string, func(string) error) error
	FilePreview(policy.BoundScope, string, string) (string, []byte, error)
	WriteFile(policy.BoundScope, string, string, string, []byte, func([]byte) error) error
}

func digest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func validID(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, e := hex.DecodeString(s)
	return e == nil
}
func terminal(s string) bool {
	switch s {
	case "succeeded", "failed", "cancelled", "interrupted", "unknown", "rejected", "expired":
		return true
	}
	return false
}
