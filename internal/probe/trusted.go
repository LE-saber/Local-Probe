package probe

import (
	"context"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"time"
)

var ErrCleanupUnconfirmed = errors.New("process_tree_cleanup_unconfirmed")

// TrustedProcessSpec is a LOCAL approved execution plan, not a model request.
// This mode has current-user OS access; it provides no filesystem/network sandbox.
// The caller owns approval, connection authorization, audit and revocation.
type TrustedProcessSpec struct {
	Executable  ExecutableDescriptor
	SHA256      string
	Args        []string
	Directory   string
	Environment []string
	Timeout     time.Duration
	OutputBytes int
	// AcquireLaunch is an optional local revision guard. It must not come from
	// wire input, and is held only until the process is created and resumed.
	AcquireLaunch func() (func(), error)
}

type TrustedProcessResult struct {
	Stdout, Stderr []byte
	ExitCode       int64
}

// RunTrustedProcess is separate from fixed probes so their original one-process,
// version-only contract stays intact. No MCP or GUI adapter exposes this API yet.
func RunTrustedProcess(ctx context.Context, spec TrustedProcessSpec) (TrustedProcessResult, error) {
	result := TrustedProcessResult{ExitCode: -1}
	if ctx == nil || validateDescriptor(spec.Executable) != nil || !filepath.IsAbs(spec.Directory) || strings.ContainsRune(spec.Directory, 0) || spec.Timeout <= 0 || spec.Timeout > 30*time.Minute || spec.OutputBytes <= 0 || spec.OutputBytes > 10<<20 || len(spec.Args) > 128 || len(spec.Environment) > 128 || len(spec.SHA256) != 64 {
		return result, ErrInvalidInput
	}
	if _, err := hex.DecodeString(spec.SHA256); err != nil {
		return result, ErrInvalidInput
	}
	for _, arg := range spec.Args {
		if len(arg) > 4096 || strings.ContainsRune(arg, 0) {
			return result, ErrInvalidInput
		}
	}
	if !processExecutionSupported() {
		return result, ErrUnsupported
	}
	ctx, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()
	var release func()
	if spec.AcquireLaunch != nil {
		var err error
		release, err = spec.AcquireLaunch()
		if err != nil || release == nil {
			return result, ErrCancelled
		}
	}
	process, err := startTrustedProcess(ctx, spec.Executable.Path, append([]string(nil), spec.Args...), spec.Directory, append([]string(nil), spec.Environment...), spec.Executable.identity, spec.SHA256)
	if release != nil {
		release()
	}
	if err != nil {
		return result, err
	}
	defer process.close()
	out, stderr := make(chan streamResult, 1), make(chan streamResult, 1)
	go captureStream(process.stdout, spec.OutputBytes, out)
	go captureStream(process.stderr, spec.OutputBytes, stderr)
	wait := make(chan processWaitResult, 1)
	go func() { code, err := process.wait(); wait <- processWaitResult{code, err} }()
	done := ctx.Done()
	var firstErr error
	stop := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
		_ = process.kill()
	}
	for out != nil || stderr != nil || wait != nil {
		select {
		case value := <-out:
			result.Stdout = value.data
			out = nil
			if value.err != nil {
				stop(value.err)
			}
		case value := <-stderr:
			result.Stderr = value.data
			stderr = nil
			if value.err != nil {
				stop(value.err)
			}
		case value := <-wait:
			result.ExitCode = value.exitCode
			wait = nil
			// Reap descendants even when the original process exits naturally.
			if err := process.kill(); err != nil && firstErr == nil {
				firstErr = ErrUnavailable
			}
			if value.err != nil && firstErr == nil {
				firstErr = ErrUnavailable
			}
		case <-done:
			stop(contextExecutionError(ctx))
			done = nil
		}
	}
	if process.reap != nil {
		if err := process.reap(); err != nil {
			return result, ErrCleanupUnconfirmed
		}
	}
	if firstErr != nil {
		return result, firstErr
	}
	if result.ExitCode != 0 {
		return result, ErrChildExit
	}
	return result, nil
}
