// Package probe contains the deliberately small, fixed-action environment
// probe used by Local-Probe.
//
// The package does not accept a command line, working directory, environment,
// or timeout from its caller.  A caller first audits one of the supported
// executable paths and then passes the resulting descriptor to a fixed probe
// action.  The descriptor is intentionally not serializable: its private file
// identity binds the path to the audit that produced it.
package probe

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ToolID is the only kind of executable that can be probed.
type ToolID string

const (
	ToolGit    ToolID = "git"
	ToolPython ToolID = "python"
	ToolNode   ToolID = "node"
	// ToolVersionGeneric is used by a command profile whose executable is
	// configured locally (for example a vendor CLI).  It still uses the
	// narrowly bounded version-flag grammar below; it is not an arbitrary
	// command tool.
	ToolVersionGeneric ToolID = "version"
)

// ErrorCode is a stable, path-free error category returned by this package.
type ErrorCode string

const (
	CodeInvalidInput     ErrorCode = "invalid_input"
	CodeNotFound         ErrorCode = "not_found"
	CodeUnavailable      ErrorCode = "unavailable"
	CodeRejected         ErrorCode = "rejected_executable"
	CodeIdentityChanged  ErrorCode = "identity_changed"
	CodeOutputLimit      ErrorCode = "output_limit"
	CodeDeadlineExceeded ErrorCode = "deadline_exceeded"
	CodeCancelled        ErrorCode = "cancelled"
	CodeInvalidOutput    ErrorCode = "invalid_output"
	CodeHashMismatch     ErrorCode = "hash_mismatch"
	CodeHashLimit        ErrorCode = "hash_limit"
	CodeUnsupported      ErrorCode = "unsupported_platform"
	CodeChildExit        ErrorCode = "child_exit"
)

// Error is intentionally path-free.  In particular, callers may safely
// return Error.Error() to an MCP client without disclosing the audited path.
type Error struct {
	Code ErrorCode
}

func (e *Error) Error() string { return string(e.Code) }

// Is lets callers use errors.Is with the package's stable categories while
// keeping the error text free of OS paths and process details.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && e != nil && t != nil && e.Code == t.Code
}

var (
	ErrInvalidInput     = &Error{Code: CodeInvalidInput}
	ErrNotFound         = &Error{Code: CodeNotFound}
	ErrUnavailable      = &Error{Code: CodeUnavailable}
	ErrRejected         = &Error{Code: CodeRejected}
	ErrIdentityChanged  = &Error{Code: CodeIdentityChanged}
	ErrOutputLimit      = &Error{Code: CodeOutputLimit}
	ErrDeadlineExceeded = &Error{Code: CodeDeadlineExceeded}
	ErrCancelled        = &Error{Code: CodeCancelled}
	ErrInvalidOutput    = &Error{Code: CodeInvalidOutput}
	ErrHashMismatch     = &Error{Code: CodeHashMismatch}
	ErrHashLimit        = &Error{Code: CodeHashLimit}
	ErrUnsupported      = &Error{Code: CodeUnsupported}
	ErrChildExit        = &Error{Code: CodeChildExit}
)

// ExecutableDescriptor is an audited absolute executable.  Path is input
// material for the trusted caller and is never copied into a probe result.
// The unexported fields make it impossible for a remote/model caller to
// fabricate an audited descriptor from JSON.
type ExecutableDescriptor struct {
	Tool ToolID
	Path string

	auditedPath string
	auditedTool ToolID
	identity    fileIdentity
}

// ToolExistsResult reports the current identity check without starting the
// executable.  A missing path is represented as Exists=false and a nil error;
// malformed or unsafe descriptors still return a stable error.
type ToolExistsResult struct {
	Tool   ToolID
	Exists bool
}

// ToolVersionResult contains only a parsed version.  Raw process output,
// executable paths, cwd, and environment are deliberately not returned.
type ToolVersionResult struct {
	Tool    ToolID
	Version string
}

// ProcessOutcome is the bounded, path-free result of one fixed process
// attempt. StdoutBytes and StderrBytes count bytes copied into the bounded
// capture buffers, not bytes on the wire or bytes written by the child after
// the limit was reached. Output buffers are intentionally not part of this
// type; they remain private to the version parser.
type ProcessOutcome struct {
	ExitCode    *int64
	TimedOut    bool
	Cancelled   bool
	DurationMS  int64
	StdoutBytes int64
	StderrBytes int64
	ErrorCode   ErrorCode
}

const (
	defaultTimeout = 2 * time.Second
	hardTimeout    = 10 * time.Second
	maxStdout      = 64 << 10
	maxStderr      = 16 << 10
	maxHashBytes   = 512 << 20
)

// VersionExecutionPolicy is a trusted local execution policy.  It is not a
// wire request: commandexec fills it from an immutable command profile.  The
// SHA256 field, when present, is checked from the same audited Windows handle
// used for the launch guard before CreateProcess is called.
type VersionExecutionPolicy struct {
	WallTimeout time.Duration
	StdoutBytes int
	StderrBytes int
	SHA256      string
}

func DefaultVersionExecutionPolicy() VersionExecutionPolicy {
	return VersionExecutionPolicy{
		WallTimeout: defaultTimeout,
		StdoutBytes: maxStdout,
		StderrBytes: maxStderr,
	}
}

// AuditExecutable verifies and snapshots an absolute local regular file for
// one of the fixed tool IDs.  It performs no process execution.  The returned
// descriptor must be retained and passed unchanged to ToolExists or
// ToolVersion.
func AuditExecutable(tool ToolID, path string) (ExecutableDescriptor, error) {
	if !validTool(tool) || path == "" {
		return ExecutableDescriptor{}, ErrInvalidInput
	}
	identity, err := captureIdentity(path)
	if err != nil {
		return ExecutableDescriptor{}, err
	}
	return ExecutableDescriptor{
		Tool:        tool,
		Path:        path,
		auditedPath: path,
		auditedTool: tool,
		identity:    identity,
	}, nil
}

// ToolExists checks only the descriptor's path and file identity.  It never
// launches a process and never searches PATH.
func ToolExists(ctx context.Context, descriptor ExecutableDescriptor) (ToolExistsResult, error) {
	result := ToolExistsResult{Tool: descriptor.Tool}
	if err := validateDescriptor(descriptor); err != nil {
		return result, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return result, ErrCancelled
	}
	current, err := captureIdentity(descriptor.Path)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return result, nil
		}
		return result, err
	}
	if !sameIdentity(descriptor.identity, current) {
		return result, ErrIdentityChanged
	}
	result.Exists = true
	return result, nil
}

// ToolVersion runs the fixed version action for a descriptor.  The process is
// started with a private empty cwd, an exact package-owned environment, bounded
// concurrent output readers, and platform process-tree supervision.
func ToolVersion(ctx context.Context, descriptor ExecutableDescriptor) (ToolVersionResult, error) {
	args, ok := fixedVersionArgs(descriptor.Tool)
	if !ok {
		return ToolVersionResult{Tool: descriptor.Tool}, ErrInvalidInput
	}
	return ToolVersionWithPolicy(ctx, descriptor, args, DefaultVersionExecutionPolicy())
}

// ToolVersionWithPolicy executes one of the fixed version argument forms.
// The caller must have obtained descriptor from AuditExecutable and must pass
// a policy constructed by trusted local code.  The function deliberately
// rejects arbitrary argument strings and unsafe policy values even though it
// is not a network-facing API.
func ToolVersionWithPolicy(ctx context.Context, descriptor ExecutableDescriptor, args []string, policy VersionExecutionPolicy) (ToolVersionResult, error) {
	result, _, err := ToolVersionWithPolicyOutcome(ctx, descriptor, args, policy)
	return result, err
}

// ToolVersionWithPolicyOutcome is the structured counterpart to
// ToolVersionWithPolicy. It preserves the old parsed-version API while
// exposing only bounded process metadata for a future local audit producer.
// The returned outcome never contains the executable path, argv, environment,
// or raw process output.
func ToolVersionWithPolicyOutcome(ctx context.Context, descriptor ExecutableDescriptor, args []string, policy VersionExecutionPolicy) (ToolVersionResult, ProcessOutcome, error) {
	result := ToolVersionResult{Tool: descriptor.Tool}
	outcome := ProcessOutcome{}
	started := time.Now()
	finish := func(err error) (ToolVersionResult, ProcessOutcome, error) {
		outcome.DurationMS = time.Since(started).Milliseconds()
		if err != nil {
			setProcessOutcomeError(&outcome, err)
		}
		return result, outcome, err
	}

	if err := validateDescriptor(descriptor); err != nil {
		return finish(err)
	}
	if err := validateVersionArgs(args); err != nil {
		return finish(err)
	}
	if err := validateVersionExecutionPolicy(policy); err != nil {
		return finish(err)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return finish(contextExecutionError(ctx))
	}
	if !processExecutionSupported() {
		return finish(ErrUnsupported)
	}

	before, err := captureIdentity(descriptor.Path)
	if err != nil {
		return finish(err)
	}
	if !sameIdentity(descriptor.identity, before) {
		return finish(ErrIdentityChanged)
	}

	cwd, err := makePrivateWorkdir()
	if err != nil {
		return finish(ErrUnavailable)
	}
	defer os.RemoveAll(cwd)
	env, err := fixedEnvironment(cwd)
	if err != nil {
		return finish(ErrUnavailable)
	}

	run, runOutcome, runErr := runFixedProcessWithPolicyOutcome(ctx, descriptor.Path, args, cwd, env, descriptor.identity, policy)
	outcome = runOutcome

	// The identity check is performed even when the child timed out or failed;
	// a replacement must never be hidden by an unrelated process error.
	after, afterErr := captureIdentity(descriptor.Path)
	if afterErr != nil {
		clearProcessOutcomeTermination(&outcome)
		return finish(ErrIdentityChanged)
	}
	if !sameIdentity(descriptor.identity, after) {
		clearProcessOutcomeTermination(&outcome)
		return finish(ErrIdentityChanged)
	}
	if runErr != nil {
		return finish(runErr)
	}

	version, ok := parseVersion(descriptor.Tool, run.stdout, run.stderr)
	if !ok {
		return finish(ErrInvalidOutput)
	}
	result.Version = version
	return finish(nil)
}

func validTool(tool ToolID) bool {
	switch tool {
	case ToolGit, ToolPython, ToolNode, ToolVersionGeneric:
		return true
	default:
		return false
	}
}

func validateDescriptor(descriptor ExecutableDescriptor) error {
	if !validTool(descriptor.Tool) || descriptor.Path == "" || descriptor.auditedPath == "" || descriptor.auditedTool == "" {
		return ErrInvalidInput
	}
	if descriptor.Path != descriptor.auditedPath || descriptor.Tool != descriptor.auditedTool || !descriptor.identity.valid {
		return ErrInvalidInput
	}
	if err := validateExecutablePath(descriptor.Path); err != nil {
		return err
	}
	return nil
}

func fixedVersionArgs(tool ToolID) ([]string, bool) {
	switch tool {
	case ToolGit, ToolPython, ToolNode, ToolVersionGeneric:
		return []string{"--version"}, true
	default:
		return nil, false
	}
}

func validateVersionArgs(args []string) error {
	if len(args) != 1 {
		return ErrInvalidInput
	}
	switch args[0] {
	case "-v", "--version", "version":
		return nil
	default:
		return ErrInvalidInput
	}
}

func validateVersionExecutionPolicy(policy VersionExecutionPolicy) error {
	if policy.WallTimeout <= 0 || policy.WallTimeout > hardTimeout ||
		policy.StdoutBytes <= 0 || policy.StdoutBytes > maxStdout ||
		policy.StderrBytes <= 0 || policy.StderrBytes > maxStderr {
		return ErrInvalidInput
	}
	if policy.SHA256 != "" {
		if len(policy.SHA256) != 64 {
			return ErrInvalidInput
		}
		if _, err := hex.DecodeString(policy.SHA256); err != nil {
			return ErrInvalidInput
		}
	}
	return nil
}

func makePrivateWorkdir() (string, error) {
	dir, err := os.MkdirTemp("", "local-probe-probe-")
	if err != nil {
		return "", err
	}
	// Best effort on Windows (where ACLs are inherited) and strict mode on
	// Unix.  The directory is newly created and is removed after one probe.
	if err := os.Chmod(dir, 0700); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

type processOutput struct {
	stdout []byte
	stderr []byte
}

type streamResult struct {
	data []byte
	err  error
}

func runFixedProcess(ctx context.Context, path string, args []string, cwd string, env []string, expected fileIdentity) (processOutput, error) {
	return runFixedProcessWithPolicy(ctx, path, args, cwd, env, expected, DefaultVersionExecutionPolicy())
}

func runFixedProcessWithPolicy(ctx context.Context, path string, args []string, cwd string, env []string, expected fileIdentity, policy VersionExecutionPolicy) (processOutput, error) {
	result, _, err := runFixedProcessWithPolicyOutcome(ctx, path, args, cwd, env, expected, policy)
	return result, err
}

func runFixedProcessWithPolicyOutcome(ctx context.Context, path string, args []string, cwd string, env []string, expected fileIdentity, policy VersionExecutionPolicy) (processOutput, ProcessOutcome, error) {
	var result processOutput
	outcome := ProcessOutcome{}
	started := time.Now()
	finish := func(err error) (processOutput, ProcessOutcome, error) {
		outcome.DurationMS = time.Since(started).Milliseconds()
		if err != nil {
			setProcessOutcomeError(&outcome, err)
		}
		return result, outcome, err
	}
	if err := validateVersionArgs(args); err != nil {
		return finish(err)
	}
	if err := validateVersionExecutionPolicy(policy); err != nil {
		return finish(err)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !processExecutionSupported() {
		return finish(ErrUnsupported)
	}
	if err := ctx.Err(); err != nil {
		return finish(contextExecutionError(ctx))
	}
	executionCtx, cancel := context.WithTimeout(ctx, policy.WallTimeout)
	defer cancel()
	process, err := startFixedProcess(executionCtx, path, args, cwd, env, expected, policy.SHA256)
	if err != nil {
		return finish(err)
	}
	defer process.close()

	stdoutCh := make(chan streamResult, 1)
	stderrCh := make(chan streamResult, 1)
	go captureStream(process.stdout, policy.StdoutBytes, stdoutCh)
	go captureStream(process.stderr, policy.StderrBytes, stderrCh)

	waitCh := make(chan processWaitResult, 1)
	go func() {
		exitCode, err := process.wait()
		waitCh <- processWaitResult{exitCode: exitCode, err: err}
	}()

	hard := time.NewTimer(hardTimeout)
	defer hard.Stop()

	var (
		stdoutDone bool
		stderrDone bool
		waitDone   bool
		waitErr    error
		waitExit   int64
		firstErr   error
		killOnce   sync.Once
	)
	kill := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
		killOnce.Do(func() { _ = process.kill() })
	}

	for !stdoutDone || !stderrDone || !waitDone {
		select {
		case out := <-stdoutCh:
			stdoutDone = true
			result.stdout = out.data
			outcome.StdoutBytes = int64(len(out.data))
			if out.err != nil {
				kill(out.err)
			}
		case out := <-stderrCh:
			stderrDone = true
			result.stderr = out.data
			outcome.StderrBytes = int64(len(out.data))
			if out.err != nil {
				kill(out.err)
			}
		case waitResult := <-waitCh:
			waitDone = true
			waitErr = waitResult.err
			waitExit = waitResult.exitCode
		case <-executionCtx.Done():
			if !waitDone {
				kill(contextExecutionError(executionCtx))
			}
		case <-hard.C:
			if !waitDone {
				kill(ErrDeadlineExceeded)
			}
		}
	}
	if firstErr != nil {
		return finish(normalizeProcessError(firstErr))
	}
	if waitErr != nil {
		return finish(ErrUnavailable)
	}
	outcome.ExitCode = int64Pointer(waitExit)
	if waitExit != 0 {
		return finish(ErrChildExit)
	}
	return finish(nil)
}

type processWaitResult struct {
	exitCode int64
	err      error
}

func contextExecutionError(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ErrDeadlineExceeded
	}
	return ErrCancelled
}

func captureStream(reader io.Reader, limit int, out chan<- streamResult) {
	var buf bytes.Buffer
	buf.Grow(minInt(limit, 4096))
	tmp := make([]byte, 32<<10)
	for {
		n, err := reader.Read(tmp)
		if n > 0 {
			remaining := limit - buf.Len()
			if remaining > 0 {
				if n > remaining {
					_, _ = buf.Write(tmp[:remaining])
				} else {
					_, _ = buf.Write(tmp[:n])
				}
			}
			if n > remaining {
				out <- streamResult{data: buf.Bytes(), err: ErrOutputLimit}
				return
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				out <- streamResult{data: buf.Bytes()}
			} else {
				out <- streamResult{data: buf.Bytes(), err: ErrUnavailable}
			}
			return
		}
	}
}

func normalizeProcessError(err error) error {
	switch {
	case errors.Is(err, ErrOutputLimit):
		return ErrOutputLimit
	case errors.Is(err, ErrDeadlineExceeded):
		return ErrDeadlineExceeded
	case errors.Is(err, ErrCancelled):
		return ErrCancelled
	case errors.Is(err, ErrChildExit):
		return ErrChildExit
	default:
		return ErrUnavailable
	}
}

func setProcessOutcomeError(outcome *ProcessOutcome, err error) {
	if outcome == nil || err == nil {
		return
	}
	switch {
	case errors.Is(err, ErrInvalidInput):
		outcome.ErrorCode = CodeInvalidInput
	case errors.Is(err, ErrNotFound):
		outcome.ErrorCode = CodeNotFound
	case errors.Is(err, ErrRejected):
		outcome.ErrorCode = CodeRejected
	case errors.Is(err, ErrIdentityChanged):
		outcome.ErrorCode = CodeIdentityChanged
	case errors.Is(err, ErrOutputLimit):
		outcome.ErrorCode = CodeOutputLimit
	case errors.Is(err, ErrDeadlineExceeded):
		outcome.ErrorCode = CodeDeadlineExceeded
		outcome.TimedOut = true
	case errors.Is(err, ErrCancelled):
		outcome.ErrorCode = CodeCancelled
		outcome.Cancelled = true
	case errors.Is(err, ErrInvalidOutput):
		outcome.ErrorCode = CodeInvalidOutput
	case errors.Is(err, ErrHashMismatch):
		outcome.ErrorCode = CodeHashMismatch
	case errors.Is(err, ErrHashLimit):
		outcome.ErrorCode = CodeHashLimit
	case errors.Is(err, ErrUnsupported):
		outcome.ErrorCode = CodeUnsupported
	case errors.Is(err, ErrChildExit):
		outcome.ErrorCode = CodeChildExit
	default:
		outcome.ErrorCode = CodeUnavailable
	}
}

func clearProcessOutcomeTermination(outcome *ProcessOutcome) {
	if outcome == nil {
		return
	}
	outcome.ExitCode = nil
	outcome.TimedOut = false
	outcome.Cancelled = false
}

func int64Pointer(value int64) *int64 {
	return &value
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

var (
	gitVersionRE    = regexp.MustCompile(`(?m)^git version ([0-9]+(?:\.[0-9]+){1,3}(?:[-+._a-zA-Z0-9]*)?)\s*$`)
	pythonVersionRE = regexp.MustCompile(`(?m)^(?:Python|python) ([0-9]+\.[0-9]+\.[0-9]+(?:[-+._a-zA-Z0-9]*)?)\s*$`)
	nodeVersionRE   = regexp.MustCompile(`(?m)^v([0-9]+\.[0-9]+\.[0-9]+(?:[-+._a-zA-Z0-9]*)?)\s*$`)
	// Generic profiles may identify a vendor CLI not known to this package.
	// Keep the accepted output intentionally narrow: one optional ASCII tool
	// label, an optional "version" word or "v" prefix, and a semantic-looking
	// numeric version.  In particular, arbitrary stdout is never returned.
	genericVersionRE = regexp.MustCompile(`(?mi)^(?:[a-z][a-z0-9_.-]*(?:\s+version)?\s+)?v?([0-9]+\.[0-9]+\.[0-9]+(?:[-+._a-zA-Z0-9]*)?)\s*$`)
)

func parseVersion(tool ToolID, stdout, stderr []byte) (string, bool) {
	combined := make([]byte, 0, len(stdout)+len(stderr)+1)
	combined = append(combined, stdout...)
	if len(stdout) > 0 && len(stderr) > 0 {
		combined = append(combined, '\n')
	}
	combined = append(combined, stderr...)
	text := strings.TrimSpace(string(combined))
	var match []string
	switch tool {
	case ToolGit:
		match = gitVersionRE.FindStringSubmatch(text)
	case ToolPython:
		match = pythonVersionRE.FindStringSubmatch(text)
	case ToolNode:
		match = nodeVersionRE.FindStringSubmatch(text)
	case ToolVersionGeneric:
		match = genericVersionRE.FindStringSubmatch(text)
	default:
		return "", false
	}
	if len(match) != 2 || len(match[1]) == 0 || len(match[1]) > 128 {
		return "", false
	}
	return match[1], true
}
