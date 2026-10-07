// Package gitprobe contains transport-free, fixed-action Git read planning
// and output parsing. It deliberately does not start Git, resolve a root
// path, authenticate a caller, or expose a raw command-line escape hatch.
//
// Every value exposed by Plan is a preview. A plan produced today is
// deliberately non-executable: repo-local clean, smudge, and process filters
// cannot all be disabled by the fixed Git flags. The plan only carries an
// opaque logical root ID; it is not a filesystem path, cwd, repository root,
// filesystem capability, or permission to invoke Git.
package gitprobe

import (
	"bytes"
	"errors"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Action is one of the two fixed Git read operations. It is not a command
// name supplied to a process runner.
type Action string

const (
	ActionStatus Action = "git_status"
	ActionDiff   Action = "git_diff"
)

// ErrorCode is a stable, path-free category for planner/parser failures.
type ErrorCode string

const (
	CodeInvalidInput        ErrorCode = "invalid_input"
	CodeUnsupportedAction   ErrorCode = "unsupported_action"
	CodeUnsupportedEncoding ErrorCode = "unsupported_encoding"
	CodeInvalidOutput       ErrorCode = "invalid_output"
	CodeOutputLimit         ErrorCode = "output_limit"
	CodeExecutionBlocked    ErrorCode = "execution_blocked"
)

// Error intentionally contains no input value, path, or process detail.
type Error struct{ Code ErrorCode }

func (e *Error) Error() string { return string(e.Code) }

func (e *Error) Is(target error) bool {
	other, ok := target.(*Error)
	return ok && e != nil && other != nil && e.Code == other.Code
}

var (
	ErrInvalidInput        = &Error{Code: CodeInvalidInput}
	ErrUnsupportedAction   = &Error{Code: CodeUnsupportedAction}
	ErrUnsupportedEncoding = &Error{Code: CodeUnsupportedEncoding}
	ErrInvalidOutput       = &Error{Code: CodeInvalidOutput}
	ErrOutputLimit         = &Error{Code: CodeOutputLimit}
	ErrExecutionBlocked    = &Error{Code: CodeExecutionBlocked}
)

const (
	maxRootIDBytes     = 128
	maxPathBytes       = 4096
	maxPaths           = 128
	maxOutputBytes     = 8 << 20
	maxEntries         = 4096
	maxHunks           = 65536
	maxDiffLines       = 1 << 20
	defaultOutputBytes = 256 << 10
	defaultEntries     = 1024
	defaultHunks       = 4096
	defaultDiffLines   = 65536
)

// Limits bound the parser's captured output and structured result. A zero
// field selects the corresponding default; negative or over-hard-limit
// values are rejected rather than silently expanded or clamped.
type Limits struct {
	MaxOutputBytes int
	MaxEntries     int
	MaxHunks       int
	MaxDiffLines   int
}

func DefaultLimits() Limits {
	return Limits{
		MaxOutputBytes: defaultOutputBytes,
		MaxEntries:     defaultEntries,
		MaxHunks:       defaultHunks,
		MaxDiffLines:   defaultDiffLines,
	}
}

func normalizeLimits(value Limits) (Limits, error) {
	defaults := DefaultLimits()
	if value.MaxOutputBytes == 0 {
		value.MaxOutputBytes = defaults.MaxOutputBytes
	}
	if value.MaxEntries == 0 {
		value.MaxEntries = defaults.MaxEntries
	}
	if value.MaxHunks == 0 {
		value.MaxHunks = defaults.MaxHunks
	}
	if value.MaxDiffLines == 0 {
		value.MaxDiffLines = defaults.MaxDiffLines
	}
	if value.MaxOutputBytes < 1 || value.MaxOutputBytes > maxOutputBytes ||
		value.MaxEntries < 1 || value.MaxEntries > maxEntries ||
		value.MaxHunks < 1 || value.MaxHunks > maxHunks ||
		value.MaxDiffLines < 1 || value.MaxDiffLines > maxDiffLines {
		return Limits{}, ErrInvalidInput
	}
	return value, nil
}

// Request selects a fixed action. There is intentionally no executable,
// command, argv, environment, cwd, or shell field.
type Request struct {
	Action Action
	// RootID is an opaque logical identifier assigned by an authorized root
	// registry. It must never be interpreted as a filesystem path, cwd, or
	// repository root by this package or a future executor.
	RootID         LogicalRootID
	Paths          []string
	MaxOutputBytes int
	MaxEntries     int
	MaxHunks       int
	MaxDiffLines   int
}

// LogicalRootID is an opaque identifier for a previously authorized root.
// It is intentionally distinct from string so callers cannot accidentally
// pass it directly to filesystem APIs without an explicit, reviewed
// conversion. The value is not a path and does not identify the process cwd.
type LogicalRootID string

func (r Request) limits() Limits {
	return Limits{
		MaxOutputBytes: r.MaxOutputBytes,
		MaxEntries:     r.MaxEntries,
		MaxHunks:       r.MaxHunks,
		MaxDiffLines:   r.MaxDiffLines,
	}
}

// EnvironmentEntry is one member of the exact, package-owned environment
// baseline shown in a plan preview. It is not a process-launch capability and
// must not be merged with caller or project environment.
type EnvironmentEntry struct {
	Name  string
	Value string
}

// ExecutionBlockedReason explains why a plan is deliberately non-executable.
// It is part of the package-owned plan state, not a caller-controlled request
// field.
type ExecutionBlockedReason string

const (
	// BlockedByRepositoryFilters covers repo-local clean/smudge and process
	// filters. Git has no portable command-line switch that disables all of
	// those config and attribute extensions for a read action.
	BlockedByRepositoryFilters ExecutionBlockedReason = "repository_filters"
)

// Plan is an immutable-by-convention fixed Git invocation preview. All slice
// accessors return defensive copies. RootID remains an opaque logical ID and
// must be resolved by a separately authorized root layer; it is never a cwd
// or repository path. PlanAction marks every plan non-executable until the
// repository-filter gap has a separately proven solution.
type Plan struct {
	action            Action
	rootID            LogicalRootID
	executable        string
	executableAllowed bool
	blockedReason     ExecutionBlockedReason
	args              []string
	env               []EnvironmentEntry
	limits            Limits
}

func (p Plan) Action() Action { return p.action }

// RootID returns the opaque logical identifier from the request. It is not a
// cwd or repository path and cannot be used as one without an explicit
// conversion outside this package.
func (p Plan) RootID() LogicalRootID { return p.rootID }

// PreviewExecutableID returns the fixed executable name that a future,
// separately authorized launcher might inspect. It does not authorize or
// enable process creation.
func (p Plan) PreviewExecutableID() string { return p.executable }

// PreviewExecutable is always false until the repository-filter gap has a
// separately proven solution. It is descriptive state, not an execution
// capability.
func (p Plan) PreviewExecutable() bool { return p.executableAllowed }

func (p Plan) PreviewBlockedReason() ExecutionBlockedReason {
	return p.blockedReason
}
func (p Plan) Limits() Limits { return p.limits }

// PreviewArgs returns a defensive copy of fixed argv text for inspection and
// documentation only. It is never a process-launch input. PlanAction keeps
// all plans blocked because repository-local filters are not universally
// disabled by the fixed Git flags.
func (p Plan) PreviewArgs() []string { return append([]string(nil), p.args...) }

// PreviewEnvironment returns a defensive copy of the package-owned baseline
// environment for inspection only. It must not be merged with caller or
// repository environment and is not a process-launch capability.
func (p Plan) PreviewEnvironment() []EnvironmentEntry {
	return append([]EnvironmentEntry(nil), p.env...)
}

// PlanAction constructs only the two fixed Git read actions. It never starts
// Git, never turns user strings into a shell command, and never returns an
// executable plan.
func PlanAction(request Request) (Plan, error) {
	if !validAction(request.Action) {
		return Plan{}, ErrUnsupportedAction
	}
	if !validID(string(request.RootID)) {
		return Plan{}, ErrInvalidInput
	}
	limits, err := normalizeLimits(request.limits())
	if err != nil {
		return Plan{}, err
	}
	if len(request.Paths) > maxPaths {
		return Plan{}, ErrInvalidInput
	}
	if request.Action == ActionStatus && len(request.Paths) != 0 {
		return Plan{}, ErrInvalidInput
	}

	paths := make([]string, len(request.Paths))
	seen := make(map[string]struct{}, len(request.Paths))
	for i, value := range request.Paths {
		if err := validateRelativePath(value); err != nil {
			return Plan{}, err
		}
		if _, ok := seen[value]; ok {
			return Plan{}, ErrInvalidInput
		}
		seen[value] = struct{}{}
		paths[i] = value
	}

	args := fixedPrefixArgs()
	switch request.Action {
	case ActionStatus:
		args = append(args,
			"status", "--porcelain=v1", "--branch", "--untracked-files=all", "--no-renames", "-z", "--")
	case ActionDiff:
		args = append(args,
			"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames", "--no-prefix", "--")
		args = append(args, paths...)
	}
	return Plan{
		action:            request.Action,
		rootID:            request.RootID,
		executable:        "git",
		executableAllowed: false,
		blockedReason:     BlockedByRepositoryFilters,
		args:              args,
		env:               fixedEnvironment(),
		limits:            limits,
	}, nil
}

func validAction(value Action) bool { return value == ActionStatus || value == ActionDiff }

func fixedPrefixArgs() []string {
	null := nullDevice()
	return []string{
		"--no-pager",
		"--no-optional-locks",
		"--literal-pathspecs",
		"-c", "core.fsmonitor=",
		"-c", "core.hooksPath=" + null,
		"-c", "core.quotePath=false",
		"-c", "diff.external=",
	}
}

func fixedEnvironment() []EnvironmentEntry {
	null := nullDevice()
	return []EnvironmentEntry{
		{Name: "GIT_CONFIG_NOSYSTEM", Value: "1"},
		{Name: "GIT_CONFIG_GLOBAL", Value: null},
		{Name: "GIT_CONFIG_COUNT", Value: "0"},
		{Name: "GIT_OPTIONAL_LOCKS", Value: "0"},
		{Name: "GIT_TERMINAL_PROMPT", Value: "0"},
		{Name: "GIT_PAGER", Value: "cat"},
		{Name: "GIT_EXTERNAL_DIFF", Value: ""},
		{Name: "GIT_DIFF_OPTS", Value: ""},
		{Name: "GIT_ATTR_NOSYSTEM", Value: "1"},
	}
}

func nullDevice() string {
	if runtime.GOOS == "windows" {
		return "NUL"
	}
	return "/dev/null"
}

func validID(value string) bool {
	if value == "" || len(value) > maxRootIDBytes || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// validateRelativePath accepts a literal root-relative Git path. A leading
// '-' is allowed because every planned path is placed after an explicit `--`;
// rejecting it would unnecessarily exclude a valid filename while the
// terminator preserves the safety boundary. Git pathspec magic and wildcards
// are rejected so the path cannot broaden the requested scope.
func validateRelativePath(value string) error {
	if value == "" || len(value) > maxPathBytes {
		return ErrInvalidInput
	}
	if !utf8.ValidString(value) {
		return ErrUnsupportedEncoding
	}
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\\`) ||
		strings.ContainsAny(value, `\:`) || strings.Contains(value, "//") {
		return ErrInvalidInput
	}
	for _, r := range value {
		if r == 0 || unicode.IsControl(r) {
			return ErrInvalidInput
		}
	}
	for _, part := range strings.Split(value, "/") {
		if !validWindowsPathPart(part) {
			return ErrInvalidInput
		}
	}
	return nil
}

func validWindowsPathPart(part string) bool {
	if part == "" || part == "." || part == ".." || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
		return false
	}
	if strings.ContainsAny(part, `<>"|*?[]`) {
		return false
	}
	base := part
	if dot := strings.IndexByte(base, '.'); dot >= 0 {
		base = base[:dot]
	}
	switch strings.ToUpper(base) {
	case "CON", "PRN", "AUX", "NUL", "CLOCK$", "CONIN$", "CONOUT$":
		return false
	}
	upper := strings.ToUpper(base)
	for _, prefix := range []string{"COM", "LPT"} {
		if strings.HasPrefix(upper, prefix) {
			n := strings.TrimPrefix(upper, prefix)
			if n == "1" || n == "2" || n == "3" || n == "4" || n == "5" || n == "6" || n == "7" || n == "8" || n == "9" || n == "¹" || n == "²" || n == "³" {
				return false
			}
		}
	}
	return true
}

// Coverage describes the bounded raw output consumed by a parser. It is not
// an MCP wire-size measurement.
type Coverage struct {
	OutputBytes     int `json:"output_bytes"`
	ReturnedEntries int `json:"returned_entries"`
	// Complete is true only when the caller reported a normal stdout EOF and
	// the capture was not truncated and the process exited successfully.
	Complete bool `json:"complete"`
}

// StatusBranch contains the branch metadata available from porcelain-v1's
// `## ...` header. Porcelain-v1 does not provide an object ID here, so this
// type intentionally has no OID field.
type StatusBranch struct {
	Head           string `json:"head,omitempty"`
	Upstream       string `json:"upstream,omitempty"`
	Ahead          int64  `json:"ahead,omitempty"`
	Behind         int64  `json:"behind,omitempty"`
	HasAheadBehind bool   `json:"has_ahead_behind"`
	Initial        bool   `json:"initial"`
	Detached       bool   `json:"detached"`
	UpstreamGone   bool   `json:"upstream_gone"`
}

// StatusEntry is one porcelain-v1 status record. Index and Worktree are the
// two single-byte Git status columns represented as strings for JSON safety.
type StatusEntry struct {
	Index    string `json:"index"`
	Worktree string `json:"worktree"`
	Path     string `json:"path"`
}

type StatusResult struct {
	SchemaVersion string        `json:"schema_version"`
	Action        Action        `json:"action"`
	Branch        StatusBranch  `json:"branch"`
	Entries       []StatusEntry `json:"entries"`
	Coverage      Coverage      `json:"coverage"`
}

// ParseStatus parses captured output from the fixed
// `status --porcelain=v1 --branch -z` shape planned by PlanAction. Its first
// record is the v1 `## ...` branch header terminated by NUL; subsequent
// NUL-delimited records are `XY path`. It deliberately does not accept
// porcelain-v2 headers or line-delimited output because the planner fixes the
// v1/NUL protocol. normalEOF must be true only after the caller observed a
// normal stdout EOF; truncated marks output cut by the capture budget, and
// exitSuccess must report a zero process exit status. A complete-looking NUL
// boundary is not enough to claim a complete result when any of those three
// conditions is false.
func ParseStatus(data []byte, limits Limits, normalEOF, truncated, exitSuccess bool) (StatusResult, error) {
	result := StatusResult{SchemaVersion: "gitprobe.v1", Action: ActionStatus, Entries: make([]StatusEntry, 0)}
	limits, err := normalizeLimits(limits)
	if err != nil {
		return StatusResult{}, err
	}
	if normalEOF && truncated {
		return StatusResult{}, ErrInvalidInput
	}
	if len(data) > limits.MaxOutputBytes {
		return StatusResult{}, ErrOutputLimit
	}
	result.Coverage = Coverage{OutputBytes: len(data), Complete: normalEOF && !truncated && exitSuccess}
	if truncated {
		return result, ErrOutputLimit
	}
	if !utf8.Valid(data) {
		return StatusResult{}, ErrUnsupportedEncoding
	}
	branchEnd := bytes.IndexByte(data, 0)
	if branchEnd < 0 {
		return StatusResult{}, ErrInvalidOutput
	}
	if err := parseBranchHeader(data[:branchEnd], &result.Branch); err != nil {
		return StatusResult{}, err
	}
	for offset := branchEnd + 1; offset < len(data); {
		end := bytes.IndexByte(data[offset:], 0)
		if end < 0 {
			return StatusResult{}, ErrInvalidOutput
		}
		record := data[offset : offset+end]
		entry, err := parseStatusRecord(record)
		if err != nil {
			return StatusResult{}, err
		}
		if len(result.Entries) >= limits.MaxEntries {
			return StatusResult{}, ErrOutputLimit
		}
		result.Entries = append(result.Entries, entry)
		offset += end + 1
	}
	result.Coverage.ReturnedEntries = len(result.Entries)
	return result, nil
}

func parseBranchHeader(line []byte, branch *StatusBranch) error {
	if len(line) < 3 || !utf8.Valid(line) || !bytes.HasPrefix(line, []byte("## ")) {
		return ErrInvalidOutput
	}
	body := string(line[3:])
	if body == "" {
		return ErrInvalidOutput
	}
	for _, value := range body {
		if unicode.IsControl(value) {
			return ErrInvalidOutput
		}
	}
	if strings.HasPrefix(body, "No commits yet on ") {
		branch.Head = strings.TrimPrefix(body, "No commits yet on ")
		branch.Initial = branch.Head != ""
		return branchHeaderValueValid(branch.Head)
	}
	if strings.HasPrefix(body, "Initial commit on ") {
		branch.Head = strings.TrimPrefix(body, "Initial commit on ")
		branch.Initial = branch.Head != ""
		return branchHeaderValueValid(branch.Head)
	}
	if body == "HEAD (no branch)" {
		branch.Detached = true
		return nil
	}
	if strings.HasPrefix(body, "HEAD detached") {
		rest := strings.TrimPrefix(body, "HEAD detached")
		if !strings.HasPrefix(rest, " ") || strings.TrimSpace(rest) == "" {
			return ErrInvalidOutput
		}
		branch.Detached = true
		return nil
	}

	tracking := body
	if open := strings.LastIndexByte(tracking, '['); open >= 0 {
		if !strings.HasSuffix(tracking, "]") || open == 0 {
			return ErrInvalidOutput
		}
		value := tracking[open+1 : len(tracking)-1]
		tracking = strings.TrimSpace(tracking[:open])
		if err := parseAheadBehind(value, branch); err != nil {
			if value != "gone" {
				return err
			}
			branch.UpstreamGone = true
		}
	}
	if separator := strings.Index(tracking, "..."); separator >= 0 {
		if separator == 0 || separator+3 == len(tracking) {
			return ErrInvalidOutput
		}
		branch.Head, branch.Upstream = tracking[:separator], tracking[separator+3:]
	} else {
		branch.Head = tracking
	}
	if err := branchHeaderValueValid(branch.Head); err != nil {
		return err
	}
	if branch.Upstream != "" {
		return branchHeaderValueValid(branch.Upstream)
	}
	if branch.HasAheadBehind || branch.UpstreamGone {
		return ErrInvalidOutput
	}
	return nil
}

func branchHeaderValueValid(value string) error {
	if value == "" || !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n") {
		return ErrInvalidOutput
	}
	return nil
}

func parseAheadBehind(value string, branch *StatusBranch) error {
	parts := strings.Split(value, ",")
	if len(parts) < 1 || len(parts) > 2 {
		return ErrInvalidOutput
	}
	var seenAhead, seenBehind bool
	for _, part := range parts {
		fields := strings.Fields(part)
		if len(fields) != 2 {
			return ErrInvalidOutput
		}
		if fields[1] == "" {
			return ErrInvalidOutput
		}
		for _, digit := range fields[1] {
			if digit < '0' || digit > '9' {
				return ErrInvalidOutput
			}
		}
		count, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || count < 0 {
			return ErrInvalidOutput
		}
		switch fields[0] {
		case "ahead":
			if seenAhead {
				return ErrInvalidOutput
			}
			seenAhead = true
			branch.Ahead = count
		case "behind":
			if seenBehind {
				return ErrInvalidOutput
			}
			seenBehind = true
			branch.Behind = count
		default:
			return ErrInvalidOutput
		}
	}
	branch.HasAheadBehind = true
	return nil
}

func parseStatusRecord(record []byte) (StatusEntry, error) {
	if len(record) < 4 || record[2] != ' ' || !validStatusByte(record[0]) || !validStatusByte(record[1]) || record[0] == ' ' && record[1] == ' ' {
		return StatusEntry{}, ErrInvalidOutput
	}
	path := string(record[3:])
	if err := validateRelativePath(path); err != nil {
		if errors.Is(err, ErrUnsupportedEncoding) {
			return StatusEntry{}, err
		}
		return StatusEntry{}, ErrInvalidOutput
	}
	return StatusEntry{Index: string(record[0]), Worktree: string(record[1]), Path: path}, nil
}

func validStatusByte(value byte) bool {
	switch value {
	case ' ', 'M', 'T', 'A', 'D', 'U', '?', '!':
		return true
	default:
		return false
	}
}

type DiffLine struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

type DiffHunk struct {
	Header string     `json:"header"`
	Lines  []DiffLine `json:"lines"`
}

type DiffFile struct {
	OldPath      string     `json:"old_path"`
	NewPath      string     `json:"new_path"`
	Binary       bool       `json:"binary,omitempty"`
	AddedLines   int        `json:"added_lines"`
	RemovedLines int        `json:"removed_lines"`
	Hunks        []DiffHunk `json:"hunks,omitempty"`
}

type DiffResult struct {
	SchemaVersion string     `json:"schema_version"`
	Action        Action     `json:"action"`
	Files         []DiffFile `json:"files"`
	Coverage      Coverage   `json:"coverage"`
}

// ParseDiff parses captured output from the bounded, no-color, no-prefix
// textual Git diff plan. normalEOF must be true only after the caller observed
// a normal stdout EOF; truncated marks output cut by the capture budget, and
// exitSuccess must report a zero process exit status. A complete-looking final
// line is not enough to claim a complete result when any of those three
// conditions is false.
func ParseDiff(data []byte, limits Limits, normalEOF, truncated, exitSuccess bool) (DiffResult, error) {
	result := DiffResult{SchemaVersion: "gitprobe.v1", Action: ActionDiff, Files: make([]DiffFile, 0)}
	limits, err := normalizeLimits(limits)
	if err != nil {
		return DiffResult{}, err
	}
	if normalEOF && truncated {
		return DiffResult{}, ErrInvalidInput
	}
	if len(data) > limits.MaxOutputBytes {
		return DiffResult{}, ErrOutputLimit
	}
	result.Coverage = Coverage{OutputBytes: len(data), Complete: normalEOF && !truncated && exitSuccess}
	if truncated {
		return result, ErrOutputLimit
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		if !utf8.Valid(data) {
			return DiffResult{}, ErrUnsupportedEncoding
		}
		return DiffResult{}, ErrInvalidOutput
	}
	var current *DiffFile
	hunkIndex := -1
	var hunkOldCount, hunkNewCount int
	var hunkOldSeen, hunkNewSeen int
	lineCount := 0
	var oldMarker, newMarker bool
	var newFile, deletedFile bool
	var oldMode, newMode bool
	var binaryOldPath, binaryNewPath string
	var binaryPathsSet bool
	var headerOldPath, headerNewPath string
	validateHunkCounts := func() error {
		if hunkIndex < 0 {
			return nil
		}
		if hunkOldSeen != hunkOldCount || hunkNewSeen != hunkNewCount {
			return ErrInvalidOutput
		}
		return nil
	}
	flush := func() error {
		if current == nil {
			return nil
		}
		if err := validateHunkCounts(); err != nil {
			return err
		}
		if oldMode != newMode || (newFile && deletedFile) || (newFile && (oldMode || newMode)) || (deletedFile && (oldMode || newMode)) {
			return ErrInvalidOutput
		}
		if oldMarker != newMarker {
			return ErrInvalidOutput
		}
		if current.Binary {
			if !binaryPathsSet || oldMarker || newMarker || hunkIndex >= 0 {
				return ErrInvalidOutput
			}
			switch {
			case newFile:
				if binaryOldPath != "/dev/null" || binaryNewPath != current.NewPath {
					return ErrInvalidOutput
				}
			case deletedFile:
				if binaryOldPath != current.OldPath || binaryNewPath != "/dev/null" {
					return ErrInvalidOutput
				}
			default:
				if binaryOldPath != current.OldPath || binaryNewPath != current.NewPath {
					return ErrInvalidOutput
				}
			}
		} else {
			if binaryPathsSet || (newFile && (!oldMarker || current.OldPath != "/dev/null")) || (deletedFile && (!newMarker || current.NewPath != "/dev/null")) {
				return ErrInvalidOutput
			}
			if current.OldPath == "/dev/null" && !newFile {
				return ErrInvalidOutput
			}
			if current.NewPath == "/dev/null" && !deletedFile {
				return ErrInvalidOutput
			}
			if !oldMarker && !newMarker && !oldMode && !newFile && !deletedFile && hunkIndex < 0 {
				return ErrInvalidOutput
			}
			if oldMarker && newMarker && !newFile && !deletedFile && hunkIndex < 0 {
				return ErrInvalidOutput
			}
		}
		if current.OldPath == "" || current.NewPath == "" {
			return ErrInvalidOutput
		}
		if current.OldPath != "/dev/null" {
			if err := validateRelativePath(current.OldPath); err != nil {
				return ErrInvalidOutput
			}
		}
		if current.NewPath != "/dev/null" {
			if err := validateRelativePath(current.NewPath); err != nil {
				return ErrInvalidOutput
			}
		}
		if len(result.Files) >= limits.MaxEntries {
			return ErrOutputLimit
		}
		result.Files = append(result.Files, *current)
		return nil
	}

	for offset := 0; offset < len(data); {
		end := bytes.IndexByte(data[offset:], '\n')
		if end < 0 {
			end = len(data) - offset
		}
		line := string(data[offset : offset+end])
		offset += end
		if offset < len(data) && data[offset] == '\n' {
			offset++
		}
		if strings.HasPrefix(line, "diff --git ") {
			if err := flush(); err != nil {
				return DiffResult{}, err
			}
			oldPath, newPath, ok := parseGitHeaderPaths(strings.TrimPrefix(line, "diff --git "))
			if !ok {
				return DiffResult{}, ErrInvalidOutput
			}
			current = &DiffFile{OldPath: oldPath, NewPath: newPath}
			headerOldPath, headerNewPath = oldPath, newPath
			oldMarker, newMarker = false, false
			newFile, deletedFile = false, false
			oldMode, newMode = false, false
			binaryOldPath, binaryNewPath = "", ""
			binaryPathsSet = false
			hunkIndex = -1
			hunkOldCount, hunkNewCount = 0, 0
			hunkOldSeen, hunkNewSeen = 0, 0
			continue
		}
		if current == nil {
			if line == "" {
				continue
			}
			return DiffResult{}, ErrInvalidOutput
		}
		switch {
		case hunkIndex < 0 && strings.HasPrefix(line, "new file mode "):
			if newFile || deletedFile || oldMode || newMode || !validModeMetadata(line, "new file mode ") {
				return DiffResult{}, ErrInvalidOutput
			}
			newFile = true
		case hunkIndex < 0 && strings.HasPrefix(line, "deleted file mode "):
			if newFile || deletedFile || oldMode || newMode || !validModeMetadata(line, "deleted file mode ") {
				return DiffResult{}, ErrInvalidOutput
			}
			deletedFile = true
		case hunkIndex < 0 && strings.HasPrefix(line, "old mode "):
			if oldMode || newMode || newFile || deletedFile || !validModeMetadata(line, "old mode ") {
				return DiffResult{}, ErrInvalidOutput
			}
			oldMode = true
		case hunkIndex < 0 && strings.HasPrefix(line, "new mode "):
			if !oldMode || newMode || newFile || deletedFile || !validModeMetadata(line, "new mode ") {
				return DiffResult{}, ErrInvalidOutput
			}
			newMode = true
		case hunkIndex < 0 && strings.HasPrefix(line, "--- "):
			if oldMarker || newMarker {
				return DiffResult{}, ErrInvalidOutput
			}
			path, ok := parseDiffPathLine(line, "--- ")
			if !ok {
				return DiffResult{}, ErrInvalidOutput
			}
			expected := headerOldPath
			if newFile {
				expected = "/dev/null"
			}
			if path != expected {
				return DiffResult{}, ErrInvalidOutput
			}
			current.OldPath = path
			oldMarker = true
		case hunkIndex < 0 && strings.HasPrefix(line, "+++ "):
			if newMarker || !oldMarker {
				return DiffResult{}, ErrInvalidOutput
			}
			path, ok := parseDiffPathLine(line, "+++ ")
			if !ok {
				return DiffResult{}, ErrInvalidOutput
			}
			expected := headerNewPath
			if deletedFile {
				expected = "/dev/null"
			}
			if path != expected {
				return DiffResult{}, ErrInvalidOutput
			}
			current.NewPath = path
			newMarker = true
		case hunkIndex < 0 && strings.HasPrefix(line, "Binary files ") && strings.HasSuffix(line, " differ"):
			// The fixed plan does not request --binary, so Git emits this
			// summary instead of an opaque binary patch. The diff --git header
			// already supplied the authoritative paths; do not split this
			// human-readable line on " and ", which is ambiguous in filenames.
			if binaryPathsSet || oldMarker || newMarker {
				return DiffResult{}, ErrInvalidOutput
			}
			oldPath, newPath, ok := parseBinarySummary(line)
			if !ok {
				return DiffResult{}, ErrInvalidOutput
			}
			binaryOldPath, binaryNewPath = oldPath, newPath
			binaryPathsSet = true
			current.Binary = true
		case hunkIndex < 0 && strings.HasPrefix(line, "GIT binary patch"):
			// --binary is not part of the fixed plan. Accepting its payload
			// would mark an opaque patch complete while silently discarding
			// its content.
			return DiffResult{}, ErrInvalidOutput
		case strings.HasPrefix(line, "similarity index ") ||
			strings.HasPrefix(line, "dissimilarity index ") ||
			strings.HasPrefix(line, "rename from ") ||
			strings.HasPrefix(line, "rename to ") ||
			strings.HasPrefix(line, "copy from ") ||
			strings.HasPrefix(line, "copy to "):
			// The planner forces --no-renames. Seeing rename/copy metadata
			// means the output does not match the fixed action contract.
			return DiffResult{}, ErrInvalidOutput
		case strings.HasPrefix(line, "@@"):
			if current.Binary || !oldMarker || !newMarker {
				return DiffResult{}, ErrInvalidOutput
			}
			if err := validateHunkCounts(); err != nil {
				return DiffResult{}, err
			}
			oldCount, newCount, ok := parseHunkHeader(line)
			if !ok {
				return DiffResult{}, ErrInvalidOutput
			}
			if len(current.Hunks) >= limits.MaxHunks {
				return DiffResult{}, ErrOutputLimit
			}
			current.Hunks = append(current.Hunks, DiffHunk{Header: line})
			hunkIndex = len(current.Hunks) - 1
			hunkOldCount, hunkNewCount = oldCount, newCount
			hunkOldSeen, hunkNewSeen = 0, 0
		case strings.HasPrefix(line, "\\ No newline at end of file"):
			if hunkIndex < 0 {
				return DiffResult{}, ErrInvalidOutput
			}
			if lineCount >= limits.MaxDiffLines {
				return DiffResult{}, ErrOutputLimit
			}
			current.Hunks[hunkIndex].Lines = append(current.Hunks[hunkIndex].Lines, DiffLine{Kind: "\\", Text: strings.TrimPrefix(line, "\\ ")})
			lineCount++
		case isDiffBodyLine(line):
			if hunkIndex < 0 {
				return DiffResult{}, ErrInvalidOutput
			}
			if lineCount >= limits.MaxDiffLines {
				return DiffResult{}, ErrOutputLimit
			}
			kind := line[:1]
			current.Hunks[hunkIndex].Lines = append(current.Hunks[hunkIndex].Lines, DiffLine{Kind: kind, Text: line[1:]})
			lineCount++
			switch kind {
			case "+":
				hunkNewSeen++
				current.AddedLines++
			case "-":
				hunkOldSeen++
				current.RemovedLines++
			case " ":
				hunkOldSeen++
				hunkNewSeen++
			}
		default:
			if hunkIndex >= 0 || !isFixedDiffMetadata(line) {
				return DiffResult{}, ErrInvalidOutput
			}
		}
	}
	if err := flush(); err != nil {
		return DiffResult{}, err
	}
	result.Coverage.ReturnedEntries = len(result.Files)
	return result, nil
}

func parseDiffPathLine(line, prefix string) (string, bool) {
	if !strings.HasPrefix(line, prefix) {
		return "", false
	}
	path, rest, ok := parseGitPathToken(strings.TrimPrefix(line, prefix))
	return path, ok && path != "" && strings.TrimSpace(rest) == ""
}

func parseBinarySummary(line string) (string, string, bool) {
	const prefix = "Binary files "
	if !strings.HasPrefix(line, prefix) || !strings.HasSuffix(line, " differ") {
		return "", "", false
	}
	value := strings.TrimSuffix(strings.TrimPrefix(line, prefix), " differ")
	oldPath, rest, ok := parseGitPathToken(value)
	if !ok {
		return "", "", false
	}
	rest = strings.TrimLeft(rest, " ")
	if !strings.HasPrefix(rest, "and ") {
		return "", "", false
	}
	newPath, rest, ok := parseGitPathToken(strings.TrimPrefix(rest, "and "))
	if !ok || strings.TrimSpace(rest) != "" {
		return "", "", false
	}
	return oldPath, newPath, oldPath != "" && newPath != ""
}

func isFixedDiffMetadata(line string) bool {
	if strings.HasPrefix(line, "index ") {
		return validIndexMetadata(line)
	}
	for _, prefix := range []string{
		"new file mode ",
		"deleted file mode ",
		"old mode ",
		"new mode ",
	} {
		if strings.HasPrefix(line, prefix) {
			return validModeMetadata(line, prefix)
		}
	}
	return false
}

func validModeMetadata(line, prefix string) bool {
	value := strings.TrimPrefix(line, prefix)
	if len(value) != 6 {
		return false
	}
	for _, digit := range value {
		if digit < '0' || digit > '7' {
			return false
		}
	}
	return true
}

func validIndexMetadata(line string) bool {
	fields := strings.Fields(strings.TrimPrefix(line, "index "))
	if len(fields) < 1 || len(fields) > 2 {
		return false
	}
	ranges := strings.Split(fields[0], "..")
	if len(ranges) != 2 || ranges[0] == "" || ranges[1] == "" {
		return false
	}
	for _, value := range ranges {
		for _, digit := range value {
			if !((digit >= '0' && digit <= '9') || (digit >= 'a' && digit <= 'f') || (digit >= 'A' && digit <= 'F')) {
				return false
			}
		}
	}
	return len(fields) == 1 || validModeMetadata(fields[1], "")
}

func parseGitHeaderPaths(value string) (string, string, bool) {
	oldPath, rest, ok := parseGitPathToken(value)
	if !ok {
		return "", "", false
	}
	newPath, rest, ok := parseGitPathToken(rest)
	if !ok || strings.TrimSpace(rest) != "" {
		return "", "", false
	}
	return oldPath, newPath, oldPath != "" && newPath != ""
}

func parseGitPathToken(value string) (string, string, bool) {
	value = strings.TrimLeft(value, " ")
	if value == "" {
		return "", "", false
	}
	if value[0] != '"' {
		separator := strings.IndexByte(value, ' ')
		if separator <= 0 {
			return value, "", true
		}
		return value[:separator], value[separator:], true
	}
	var decoded strings.Builder
	for i := 1; i < len(value); i++ {
		switch value[i] {
		case '"':
			rest := value[i+1:]
			if rest != "" && rest[0] != ' ' {
				return "", "", false
			}
			return decoded.String(), rest, true
		case '\\':
			i++
			if i >= len(value) {
				return "", "", false
			}
			switch value[i] {
			case '\\', '"':
				decoded.WriteByte(value[i])
			case 'a':
				decoded.WriteByte('\a')
			case 'b':
				decoded.WriteByte('\b')
			case 't':
				decoded.WriteByte('\t')
			case 'n':
				decoded.WriteByte('\n')
			case 'v':
				decoded.WriteByte('\v')
			case 'f':
				decoded.WriteByte('\f')
			case 'r':
				decoded.WriteByte('\r')
			case '0', '1', '2', '3', '4', '5', '6', '7':
				valueByte := value[i] - '0'
				for count := 0; count < 2 && i+1 < len(value) && value[i+1] >= '0' && value[i+1] <= '7'; count++ {
					i++
					valueByte = valueByte*8 + value[i] - '0'
				}
				decoded.WriteByte(valueByte)
			default:
				return "", "", false
			}
		default:
			decoded.WriteByte(value[i])
		}
	}
	return "", "", false
}

var hunkHeaderPattern = regexp.MustCompile(`^@@ -([0-9]+)(?:,([0-9]+))? \+([0-9]+)(?:,([0-9]+))? @@(?: .*)?$`)

// parseHunkHeader parses the complete unified-diff range header. An omitted
// count means one line; zero is valid for an insertion/deletion range. Starts
// are parsed as unsigned decimal values to reject malformed/overflowing
// headers, while counts are bounded by the parser's line budget so a hostile
// header cannot create an unbounded expectation.
func parseHunkHeader(value string) (oldCount, newCount int, ok bool) {
	match := hunkHeaderPattern.FindStringSubmatch(value)
	if len(match) != 5 {
		return 0, 0, false
	}
	for _, start := range []string{match[1], match[3]} {
		if _, err := strconv.ParseUint(start, 10, 64); err != nil {
			return 0, 0, false
		}
	}
	parseCount := func(value string) (int, bool) {
		if value == "" {
			return 1, true
		}
		parsed, err := strconv.ParseUint(value, 10, 64)
		if err != nil || parsed > uint64(maxDiffLines) {
			return 0, false
		}
		return int(parsed), true
	}
	var valid bool
	if oldCount, valid = parseCount(match[2]); !valid {
		return 0, 0, false
	}
	if newCount, valid = parseCount(match[4]); !valid {
		return 0, 0, false
	}
	return oldCount, newCount, true
}

func isDiffBodyLine(value string) bool {
	if value == "" {
		return false
	}
	switch value[0] {
	case ' ', '+', '-':
		return true
	default:
		return false
	}
}

// Compile-time use of errors keeps wrapping behavior explicit for callers
// that use errors.Is with the stable categories.
var _ error = (*Error)(nil)
