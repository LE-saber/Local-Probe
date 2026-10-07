// Package auditreader reads the local audit.v2 JSONL projection for desktop
// diagnostics.  It is deliberately a one-way, bounded reader: it accepts one
// explicit directory, recognizes only the audit sink's fixed file names, and
// exposes only the fields approved for a diagnostics/support view.
package auditreader

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/LE-saber/Local-Probe/internal/audit"
	"github.com/LE-saber/Local-Probe/internal/desktopadmin"
)

const (
	// ActiveFileName and ArchivePrefix are part of the reader contract.  A
	// caller cannot select an arbitrary prefix and thereby broaden the files
	// that are inspected.
	ActiveFileName = "audit.jsonl"
	ArchivePrefix  = "audit-"
	ArchiveSuffix  = ".jsonl"

	defaultMaxFiles     = 16
	defaultMaxBytes     = 512 << 10
	defaultMaxLines     = 8192
	defaultMaxRecords   = desktopadmin.MaxItems
	defaultMaxLineBytes = 64 << 10

	hardMaxFiles     = 64
	hardMaxBytes     = 4 << 20
	hardMaxLines     = 100000
	hardMaxLineBytes = 256 << 10
	maxPathBytes     = 4096

	// maxCounter and maxDuration mirror the audit package's validation
	// bounds.  They are kept local because audit's wire type is intentionally
	// private.
	maxCounter  = int64(1) << 50
	maxDuration = int64((24 * time.Hour) / time.Millisecond)
)

var (
	archiveRE = regexp.MustCompile(`^audit-([0-9]{20})\.jsonl$`)

	ErrInvalidDirectory = errors.New("invalid audit reader directory")
	ErrInvalidLimits    = errors.New("invalid audit reader limits")
	ErrUnsafeFile       = errors.New("unsafe audit file")
	ErrRead             = errors.New("audit read failed")
	// ErrCanceled aliases context.Canceled so callers can use errors.Is with
	// the standard context sentinel while still documenting this boundary.
	ErrCanceled      = context.Canceled
	ErrSensitiveData = errors.New("sensitive audit data rejected")
	ErrInvalidRecord = errors.New("invalid audit record")
)

// Limits bounds every resource controlled by the reader.  A zero field uses
// the corresponding DefaultLimits value.  Explicit values above the hard
// bounds are rejected instead of silently weakening the contract.
type Limits struct {
	MaxFiles     int
	MaxBytes     int64
	MaxLines     int
	MaxRecords   int
	MaxLineBytes int
}

// DefaultLimits returns the conservative limits used by New.
func DefaultLimits() Limits {
	return Limits{
		MaxFiles: defaultMaxFiles, MaxBytes: defaultMaxBytes,
		MaxLines: defaultMaxLines, MaxRecords: defaultMaxRecords,
		MaxLineBytes: defaultMaxLineBytes,
	}
}

func normalizeLimits(value Limits) (Limits, error) {
	defaults := DefaultLimits()
	if value.MaxFiles == 0 {
		value.MaxFiles = defaults.MaxFiles
	}
	if value.MaxBytes == 0 {
		value.MaxBytes = defaults.MaxBytes
	}
	if value.MaxLines == 0 {
		value.MaxLines = defaults.MaxLines
	}
	if value.MaxRecords == 0 {
		value.MaxRecords = defaults.MaxRecords
	}
	if value.MaxLineBytes == 0 {
		value.MaxLineBytes = defaults.MaxLineBytes
	}
	if value.MaxFiles < 1 || value.MaxFiles > hardMaxFiles ||
		value.MaxBytes < 1 || value.MaxBytes > hardMaxBytes ||
		value.MaxLines < 1 || value.MaxLines > hardMaxLines ||
		value.MaxRecords < 1 || value.MaxRecords > desktopadmin.MaxItems ||
		value.MaxLineBytes < 1 || value.MaxLineBytes > hardMaxLineBytes {
		return Limits{}, ErrInvalidLimits
	}
	return value, nil
}

// Entry contains only the allowlisted, path-free portion of one audit.v2
// event.  Event IDs, actions, command IDs, argv, environment, output and
// network details are intentionally not represented.
type Entry struct {
	Timestamp  time.Time       `json:"timestamp"`
	Component  audit.Component `json:"component"`
	Type       audit.EventType `json:"type"`
	Severity   audit.Severity  `json:"severity"`
	Outcome    audit.Outcome   `json:"outcome"`
	ErrorCode  string          `json:"error_code,omitempty"`
	Connection string          `json:"connection,omitempty"`
	Profile    string          `json:"profile,omitempty"`
	Revision   string          `json:"revision,omitempty"`
	DurationMS int64           `json:"duration_ms"`
	Budget     audit.Budget    `json:"budget"`
	Command    *CommandCounts  `json:"command,omitempty"`

	// ID aliases make the in-process shape consistent with desktopadmin while
	// keeping the compact audit wire names above.  They are not serialized.
	ConnectionID string `json:"-"`
	ProfileID    string `json:"-"`
}

// CommandCounts is limited to process output byte counts; output content is
// never read from or copied out of the audit stream.
type CommandCounts struct {
	StdoutBytes int64 `json:"stdout_bytes"`
	StderrBytes int64 `json:"stderr_bytes"`
}

// Summary reports only numeric/bool read health.  It contains no file name,
// path, operating-system error, or source text.
type Summary struct {
	FilesExamined      int   `json:"files_examined"`
	FilesSkipped       int   `json:"files_skipped"`
	FilesRejected      int   `json:"files_rejected"`
	ReadErrors         int   `json:"read_errors"`
	BytesRead          int64 `json:"bytes_read"`
	LinesScanned       int   `json:"lines_scanned"`
	RecordsParsed      int   `json:"records_parsed"`
	RecordsReturned    int   `json:"records_returned"`
	CorruptLines       int   `json:"corrupt_lines"`
	TruncatedLines     int   `json:"truncated_lines"`
	UnknownFieldLines  int   `json:"unknown_field_lines"`
	FileLimitReached   bool  `json:"file_limit_reached"`
	ByteLimitReached   bool  `json:"byte_limit_reached"`
	LineLimitReached   bool  `json:"line_limit_reached"`
	RecordLimitReached bool  `json:"record_limit_reached"`
}

// Validate checks that a summary remains a bounded local projection.  It is
// exported so supportbundle can perform its second validation pass without
// accessing reader internals.
func (s Summary) Validate() error {
	if s.FilesExamined < 0 || s.FilesSkipped < 0 || s.FilesRejected < 0 || s.ReadErrors < 0 ||
		s.BytesRead < 0 || s.LinesScanned < 0 || s.RecordsParsed < 0 || s.RecordsReturned < 0 ||
		s.CorruptLines < 0 || s.TruncatedLines < 0 || s.UnknownFieldLines < 0 ||
		s.RecordsReturned > desktopadmin.MaxItems || s.BytesRead > hardMaxBytes {
		return ErrInvalidRecord
	}
	return nil
}

// Report is the bounded result of a read.  Entries are sorted newest first.
type Report struct {
	Entries []Entry `json:"entries"`
	Summary Summary `json:"summary"`
}

// Reader is safe to reuse.  It keeps no open file and performs no writes.
type Reader struct {
	directory string
	limits    Limits
}

var _ desktopadmin.DiagnosticsSource = (*Reader)(nil)

// New constructs a reader for one explicit, existing directory.  The
// directory and every existing ancestor must not be a symlink/reparse point.
func New(directory string, limits Limits) (*Reader, error) {
	clean, err := validateDirectory(directory)
	if err != nil {
		return nil, err
	}
	normalized, err := normalizeLimits(limits)
	if err != nil {
		return nil, err
	}
	return &Reader{directory: clean, limits: normalized}, nil
}

// NewReader is an explicit alias for callers that prefer constructor-style
// naming.
func NewReader(directory string, limits Limits) (*Reader, error) { return New(directory, limits) }

// Limits returns a copy of the effective limits.
func (r *Reader) Limits() Limits {
	if r == nil {
		return Limits{}
	}
	return r.limits
}

// Read reads at most the configured files/bytes/lines/records and returns a
// sanitized report.  Context cancellation is checked between files and lines.
func (r *Reader) Read(ctx context.Context) (Report, error) {
	if r == nil || r.directory == "" {
		return Report{}, ErrInvalidDirectory
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := contextErr(ctx); err != nil {
		return Report{}, err
	}
	if err := validateDirectoryStillSafe(r.directory); err != nil {
		return Report{}, err
	}

	files, summary, err := r.selectFiles()
	if err != nil {
		return Report{}, err
	}
	entries := make([]Entry, 0, minInt(r.limits.MaxRecords, 32))
	state := readState{summary: summary, remainingBytes: r.limits.MaxBytes, remainingLines: r.limits.MaxLines, remainingRecords: r.limits.MaxRecords}
	for _, candidate := range files {
		if err := contextErr(ctx); err != nil {
			return Report{}, err
		}
		if state.remainingBytes <= 0 || state.remainingLines <= 0 || state.remainingRecords <= 0 {
			break
		}
		before := len(entries)
		if err := r.readFile(ctx, candidate.path, &state, &entries); err != nil {
			if errors.Is(err, ErrCanceled) {
				return Report{}, err
			}
			// A file-level failure is reported as a bounded count.  The
			// path and OS error never cross this package boundary.
			if errors.Is(err, ErrUnsafeFile) {
				state.summary.FilesRejected++
			} else {
				state.summary.ReadErrors++
			}
			_ = before
		}
	}
	if state.remainingBytes <= 0 {
		state.summary.ByteLimitReached = true
	}
	if state.remainingLines <= 0 {
		state.summary.LineLimitReached = true
	}
	if state.remainingRecords <= 0 {
		state.summary.RecordLimitReached = true
	}
	state.summary.RecordsReturned = len(entries)
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Timestamp.After(entries[j].Timestamp) })
	state.summary.RecordsReturned = len(entries)
	if err := state.summary.Validate(); err != nil {
		return Report{}, ErrRead
	}
	return Report{Entries: entries, Summary: state.summary}, nil
}

// ReadWithMaxItems reads a report with an additional caller-supplied record
// ceiling.  It never weakens the limits configured on the Reader: a smaller
// request is applied before scanning the files, so a Preview refresh cannot
// make the audit reader parse an unbounded number of records and trim them
// only after the fact.  A zero limit means the package-wide desktopadmin
// maximum, matching ReadDiagnostics.
func (r *Reader) ReadWithMaxItems(ctx context.Context, maxItems int) (Report, error) {
	if r == nil {
		return Report{}, ErrInvalidDirectory
	}
	if maxItems == 0 {
		maxItems = desktopadmin.MaxItems
	}
	if maxItems < 1 || maxItems > desktopadmin.MaxItems {
		return Report{}, ErrInvalidLimits
	}
	limits := r.Limits()
	if limits.MaxRecords > maxItems {
		limits.MaxRecords = maxItems
	}
	bounded := &Reader{directory: r.directory, limits: limits}
	return bounded.Read(ctx)
}

// ReadDiagnostics adapts the sanitized entries to desktopadmin's local-only
// source interface.  It intentionally returns no budget/output fields because
// that interface is a narrow GUI diagnostics projection.
func (r *Reader) ReadDiagnostics(ctx context.Context, maxItems int) ([]desktopadmin.DiagnosticRecord, error) {
	if maxItems == 0 {
		maxItems = desktopadmin.MaxItems
	}
	if maxItems < 1 || maxItems > desktopadmin.MaxItems {
		return nil, desktopadmin.ErrInvalidLimit
	}
	report, err := r.ReadWithMaxItems(ctx, maxItems)
	if err != nil {
		return nil, err
	}
	result := make([]desktopadmin.DiagnosticRecord, 0, len(report.Entries))
	for _, entry := range report.Entries {
		record, err := desktopadmin.NewDiagnosticRecord(entry.Timestamp, desktopComponent(entry.Component), desktopSeverity(entry.Severity), desktopOutcome(entry.Outcome), entry.ErrorCode, desktopIdentifier(entry.Connection), desktopIdentifier(entry.Profile), desktopIdentifier(entry.Revision), entry.DurationMS)
		if err != nil {
			return nil, ErrInvalidRecord
		}
		result = append(result, record)
	}
	return result, nil
}

type selectedFile struct {
	path string
	seq  uint64
}

type readState struct {
	summary          Summary
	remainingBytes   int64
	remainingLines   int
	remainingRecords int
}

type wireEvent struct {
	TS                 string                   `json:"ts"`
	Event              string                   `json:"event"`
	Schema             string                   `json:"schema"`
	Instance           string                   `json:"instance"`
	Correlation        string                   `json:"correlation"`
	Parent             string                   `json:"parent,omitempty"`
	Component          audit.Component          `json:"component"`
	Type               audit.EventType          `json:"type"`
	Action             string                   `json:"action,omitempty"`
	Class              string                   `json:"class"`
	Severity           audit.Severity           `json:"severity"`
	Outcome            audit.Outcome            `json:"outcome"`
	ErrorCode          string                   `json:"error_code,omitempty"`
	DurationMS         int64                    `json:"duration_ms"`
	Connection         string                   `json:"connection,omitempty"`
	Profile            string                   `json:"profile,omitempty"`
	Revision           string                   `json:"revision,omitempty"`
	Budget             audit.Budget             `json:"budget"`
	CommandID          string                   `json:"command_id,omitempty"`
	VariantID          string                   `json:"variant_id,omitempty"`
	IdentityDigest     string                   `json:"identity_digest,omitempty"`
	ExitCode           *int64                   `json:"exit_code,omitempty"`
	TimedOut           bool                     `json:"timed_out,omitempty"`
	Cancelled          bool                     `json:"cancelled,omitempty"`
	Bytes              *audit.CommandBytes      `json:"bytes,omitempty"`
	NetworkEnforcement audit.NetworkEnforcement `json:"network_enforcement,omitempty"`
}

func (r *Reader) selectFiles() ([]selectedFile, Summary, error) {
	file, err := os.Open(r.directory)
	if err != nil {
		return nil, Summary{}, ErrRead
	}
	defer file.Close()

	selected := make([]selectedFile, 0, r.limits.MaxFiles)
	validCount := 0
	summary := Summary{}
	for {
		entries, readErr := file.ReadDir(64)
		for _, entry := range entries {
			name := entry.Name()
			seq, isArchive, matches := archiveSequence(name)
			if !matches {
				continue
			}
			path := filepath.Join(r.directory, name)
			summary.FilesExamined++
			info, statErr := os.Lstat(path)
			if statErr != nil || !regularNonReparse(info) {
				summary.FilesRejected++
				continue
			}
			if isArchive && seq == 0 {
				summary.FilesRejected++
				continue
			}
			validCount++
			candidate := selectedFile{path: path, seq: seq}
			if !isArchive {
				// There is only one active name.  Keep it ahead of
				// archives so it remains useful when MaxFiles is one.
				if len(selected) < r.limits.MaxFiles {
					selected = append(selected, candidate)
				} else {
					// Replace the oldest archive, never the active file.
					oldest := oldestArchiveIndex(selected)
					if oldest >= 0 {
						selected[oldest] = candidate
					}
				}
				continue
			}
			if len(selected) < r.limits.MaxFiles {
				selected = append(selected, candidate)
				continue
			}
			oldest := oldestArchiveIndex(selected)
			if oldest >= 0 && seq > selected[oldest].seq {
				selected[oldest] = candidate
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return nil, Summary{}, ErrRead
		}
	}
	if validCount > len(selected) {
		summary.FilesSkipped = validCount - len(selected)
		summary.FileLimitReached = true
	}
	sort.Slice(selected, func(i, j int) bool {
		if selected[i].seq == 0 {
			return true
		}
		if selected[j].seq == 0 {
			return false
		}
		return selected[i].seq > selected[j].seq
	})
	return selected, summary, nil
}

func archiveSequence(name string) (uint64, bool, bool) {
	if name == ActiveFileName {
		return 0, false, true
	}
	matches := archiveRE.FindStringSubmatch(name)
	if len(matches) != 2 {
		return 0, false, false
	}
	seq, err := strconv.ParseUint(matches[1], 10, 64)
	if err != nil {
		return 0, true, true
	}
	return seq, true, true
}

func oldestArchiveIndex(files []selectedFile) int {
	index := -1
	for i, file := range files {
		if file.seq == 0 {
			continue
		}
		if index < 0 || file.seq < files[index].seq {
			index = i
		}
	}
	return index
}

func (r *Reader) readFile(ctx context.Context, path string, state *readState, entries *[]Entry) error {
	file, before, err := openRegularAuditFile(path)
	if err != nil {
		return ErrUnsafeFile
	}
	defer file.Close()
	if err := contextErr(ctx); err != nil {
		return err
	}
	startBytes := state.summary.BytesRead
	startEntries := len(*entries)
	startParsed := state.summary.RecordsParsed
	reader := bufio.NewReaderSize(&limitedReader{reader: file, remaining: state.remainingBytes, consumed: &state.summary.BytesRead}, 32<<10)
	fileSize := before.Size()
	for state.remainingBytes > 0 && state.remainingLines > 0 && state.remainingRecords > 0 {
		if err := contextErr(ctx); err != nil {
			return err
		}
		line, complete, tooLong, readErr := readLineBounded(reader, r.limits.MaxLineBytes)
		state.remainingBytes = r.limits.MaxBytes - state.summary.BytesRead
		if readErr != nil {
			return ErrRead
		}
		if line == nil && !complete {
			break
		}
		state.remainingLines--
		state.summary.LinesScanned++
		if tooLong {
			state.summary.TruncatedLines++
			state.summary.CorruptLines++
			continue
		}
		fileBytes := state.summary.BytesRead - startBytes
		if !complete && fileSize > fileBytes {
			state.summary.TruncatedLines++
			state.summary.CorruptLines++
			state.summary.ByteLimitReached = true
			break
		}
		if len(bytes.TrimSpace(line)) == 0 {
			state.summary.CorruptLines++
			continue
		}
		var event wireEvent
		if err := decodeWireEvent(line, &event); err != nil {
			if strings.Contains(err.Error(), "unknown field") {
				state.summary.UnknownFieldLines++
			}
			state.summary.CorruptLines++
			continue
		}
		state.summary.RecordsParsed++
		entry, err := sanitizeEvent(event)
		if err != nil {
			state.summary.CorruptLines++
			continue
		}
		*entries = append(*entries, entry)
		state.remainingRecords--
	}
	if state.remainingLines <= 0 {
		state.summary.LineLimitReached = true
	}
	if state.remainingRecords <= 0 {
		state.summary.RecordLimitReached = true
	}
	if state.remainingBytes <= 0 {
		state.summary.ByteLimitReached = true
	}
	// Recheck the name after reading.  A replacement is never surfaced as
	// valid data; the records already parsed are retained as a bounded view.
	after, statErr := os.Lstat(path)
	if statErr != nil || !regularNonReparse(after) || !os.SameFile(before, after) {
		appended := len(*entries) - startEntries
		*entries = (*entries)[:startEntries]
		state.summary.RecordsParsed = startParsed
		state.remainingRecords += appended
		return ErrUnsafeFile
	}
	return nil
}

type limitedReader struct {
	reader    io.Reader
	remaining int64
	consumed  *int64
}

func (r *limitedReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.reader.Read(p)
	if n > 0 {
		r.remaining -= int64(n)
		*r.consumed += int64(n)
	}
	return n, err
}

func readLineBounded(reader *bufio.Reader, maxBytes int) ([]byte, bool, bool, error) {
	var line []byte
	tooLong := false
	for {
		fragment, err := reader.ReadSlice('\n')
		terminated := err == nil
		if terminated && len(fragment) > 0 && fragment[len(fragment)-1] == '\n' {
			fragment = fragment[:len(fragment)-1]
			if len(fragment) > 0 && fragment[len(fragment)-1] == '\r' {
				fragment = fragment[:len(fragment)-1]
			}
		}
		if len(fragment) > 0 {
			if len(line) < maxBytes {
				remaining := maxBytes - len(line)
				if len(fragment) > remaining {
					line = append(line, fragment[:remaining]...)
					tooLong = true
				} else {
					line = append(line, fragment...)
				}
			} else {
				tooLong = true
			}
		}
		if terminated {
			return line, true, tooLong, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			return line, false, tooLong, nil
		}
		return nil, false, tooLong, err
	}
}

func decodeWireEvent(line []byte, event *wireEvent) error {
	if !utf8.Valid(line) || hasDuplicateObjectKey(line) {
		return ErrInvalidRecord
	}
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(event); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return ErrInvalidRecord
	}
	return nil
}

// hasDuplicateObjectKey rejects duplicate JSON members at every nesting
// level.  encoding/json otherwise keeps the last value, which is undesirable
// for a fixed allowlist because a producer could make a record's meaning
// depend on decoder details.  Malformed input is handled by the normal decode
// path, so a parser error here simply returns false.
func hasDuplicateObjectKey(data []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walk func(json.Token) bool
	walk = func(token json.Token) bool {
		delim, ok := token.(json.Delim)
		if !ok {
			return false
		}
		switch delim {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return false
				}
				name, ok := key.(string)
				if !ok {
					return false
				}
				if _, exists := seen[name]; exists {
					return true
				}
				seen[name] = struct{}{}
				value, err := decoder.Token()
				if err != nil {
					return false
				}
				if walk(value) {
					return true
				}
			}
			_, _ = decoder.Token()
		case '[':
			for decoder.More() {
				value, err := decoder.Token()
				if err != nil {
					return false
				}
				if walk(value) {
					return true
				}
			}
			_, _ = decoder.Token()
		}
		return false
	}
	first, err := decoder.Token()
	if err != nil {
		return false
	}
	return walk(first)
}

func sanitizeEvent(event wireEvent) (Entry, error) {
	if event.Schema != audit.SchemaVersionV2 || !validSafeID(event.Event, false) || !validSafeID(event.Instance, false) || !validSafeID(event.Correlation, false) {
		return Entry{}, ErrInvalidRecord
	}
	if event.Parent != "" && !validSafeID(event.Parent, false) {
		return Entry{}, ErrInvalidRecord
	}
	if event.Action != "" && !validSafeAtom(event.Action, 128) {
		return Entry{}, ErrInvalidRecord
	}
	if !validEnum(event.Component, audit.ComponentMCP, audit.ComponentAuth, audit.ComponentPolicy, audit.ComponentFSSearch, audit.ComponentCommand, audit.ComponentNetworkTunnel, audit.ComponentServiceConfig, audit.ComponentError) ||
		!validEnum(event.Type, audit.EventMCPCall, audit.EventMCPResult, audit.EventMCPList, audit.EventAuthAccept, audit.EventAuthReject, audit.EventPolicyDecision, audit.EventSearch, audit.EventCommandAdmission, audit.EventCommandStart, audit.EventCommandResult, audit.EventCommandReject, audit.EventCommandExit, audit.EventNetworkConnect, audit.EventTunnelState, audit.EventConfigChange, audit.EventAppError) ||
		!validEnum(event.Class, "normal", "security") ||
		!validEnum(event.Severity, audit.SeverityDebug, audit.SeverityInfo, audit.SeverityWarn, audit.SeverityError, audit.SeverityCritical) ||
		!validEnum(event.Outcome, audit.OutcomeStarted, audit.OutcomeSucceeded, audit.OutcomeFailed, audit.OutcomeRejected, audit.OutcomeDegraded) {
		return Entry{}, ErrInvalidRecord
	}
	when, err := time.Parse(time.RFC3339Nano, event.TS)
	if err != nil || when.IsZero() {
		return Entry{}, ErrInvalidRecord
	}
	when = when.UTC()
	if event.DurationMS < 0 || event.DurationMS > maxDuration || !validBudget(event.Budget) || !validSafeID(event.Connection, true) || !validSafeID(event.Profile, true) || !validSafeID(event.Revision, true) {
		return Entry{}, ErrInvalidRecord
	}
	if event.ErrorCode != "" && !validErrorCode(event.ErrorCode) {
		return Entry{}, ErrInvalidRecord
	}
	if event.CommandID != "" && !validSafeID(event.CommandID, false) || event.VariantID != "" && !validSafeID(event.VariantID, false) {
		return Entry{}, ErrInvalidRecord
	}
	if event.IdentityDigest != "" && !validDigest(event.IdentityDigest) {
		return Entry{}, ErrInvalidRecord
	}
	if event.NetworkEnforcement != "" && !validEnum(event.NetworkEnforcement, audit.NetworkEnforcementVerified, audit.NetworkEnforcementUnavailable, audit.NetworkEnforcementRejected, audit.NetworkEnforcementNotChecked) {
		return Entry{}, ErrInvalidRecord
	}
	if event.ExitCode != nil && (*event.ExitCode < 0 || uint64(*event.ExitCode) > uint64(^uint32(0))) {
		return Entry{}, ErrInvalidRecord
	}
	if event.Bytes != nil {
		if event.Type != audit.EventCommandResult || event.Bytes.Stdout < 0 || event.Bytes.Stdout > maxCounter || event.Bytes.Stderr < 0 || event.Bytes.Stderr > maxCounter {
			return Entry{}, ErrInvalidRecord
		}
	}
	entry := Entry{Timestamp: when, Component: event.Component, Type: event.Type, Severity: event.Severity, Outcome: event.Outcome, ErrorCode: event.ErrorCode, Connection: event.Connection, Profile: event.Profile, Revision: event.Revision, DurationMS: event.DurationMS, Budget: event.Budget, ConnectionID: event.Connection, ProfileID: event.Profile}
	if event.Bytes != nil {
		entry.Command = &CommandCounts{StdoutBytes: event.Bytes.Stdout, StderrBytes: event.Bytes.Stderr}
	}
	return entry, nil
}

func validBudget(value audit.Budget) bool {
	for _, counter := range []int64{value.WireInBytes, value.WireOutBytes, value.LogicalReadBytes, value.ReturnedBytes, value.LimitBytes} {
		if counter < 0 || counter > maxCounter {
			return false
		}
	}
	return true
}

func validErrorCode(value string) bool {
	if !validSafeAtom(value, 64) {
		return false
	}
	switch value {
	case "invalid_request", "denied", "not_found", "unsupported_type", "unsupported_encoding", "stale_version", "budget_exhausted", "deadline_exceeded", "cancelled", "unavailable", "auth_failed", "connection_refused", "port_conflict", "config_invalid", "queue_full", "child_exit", "identity_changed", "output_limit", "invalid_input", "rejected_executable", "invalid_output", "hash_mismatch", "hash_limit", "unsupported_platform":
		return true
	default:
		return false
	}
}

func validSafeID(value string, optional bool) bool {
	if value == "" {
		return optional
	}
	return validSafeAtom(value, 128)
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'f' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func validSafeAtom(value string, max int) bool {
	if value == "" || len(value) > max || !utf8.ValidString(value) || containsSensitive(value) || looksLikePath(value) {
		return false
	}
	for _, r := range value {
		if r > unicodeMaxASCII || r == ' ' || r == '\t' || r == '\r' || r == '\n' || r < 0x20 {
			return false
		}
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._:/+-", r)) {
			return false
		}
	}
	return true
}

const unicodeMaxASCII = rune(0x7f)

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
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\\`) || strings.HasPrefix(strings.ToLower(value), "file:") {
		return true
	}
	return len(value) >= 3 && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) && value[1] == ':' && (value[2] == '\\' || value[2] == '/')
}

func validEnum[T comparable](value T, allowed ...T) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func desktopComponent(value audit.Component) desktopadmin.DiagnosticComponent {
	return desktopadmin.DiagnosticComponent(value)
}

func desktopSeverity(value audit.Severity) desktopadmin.DiagnosticSeverity {
	return desktopadmin.DiagnosticSeverity(value)
}

func desktopOutcome(value audit.Outcome) desktopadmin.DiagnosticOutcome {
	return desktopadmin.DiagnosticOutcome(value)
}

func desktopIdentifier(value string) string {
	if value == "" {
		return ""
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return ""
		}
	}
	return value
}

func contextErr(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return ErrCanceled
	}
	return nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
