package readcore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sync"
	"sync/atomic"
	"unicode/utf8"
)

type Engine struct {
	source Source
	limits Limits
}

func New(source Source, limits Limits) (*Engine, error) {
	if source == nil {
		return nil, fmt.Errorf("source is required")
	}
	if err := limits.validate(); err != nil {
		return nil, err
	}
	return &Engine{source: source, limits: limits}, nil
}

// ReadBatch uses deterministic max-min allocation before scheduling. Unused
// allocations from short files/errors are NOT redistributed in this version.
// Already completed items may survive cancellation; no new I/O starts once a
// worker observes it. Sources must honor context; Go cannot interrupt arbitrary
// blocked OS ReaderAt calls. There is intentionally no network/file opener here.
func (e *Engine) ReadBatch(ctx context.Context, scope Scope, requests []Request) (BatchResult, error) {
	out := BatchResult{SchemaVersion: "readcore.v0"}
	if !validID(scope.connection) || !validID(scope.revision) || len(scope.roots) == 0 {
		return out, issue("denied", "authenticated scope required")
	}
	if len(requests) == 0 || len(requests) > e.limits.MaxItems {
		return out, issue("invalid_request", "invalid batch size")
	}
	requests = append([]Request(nil), requests...)
	out.Items = make([]Result, len(requests))
	demands := make([]int, len(requests))
	for i, req := range requests {
		if problem := validateRequest(req, scope, e.limits.MaxItemBytes); problem != nil {
			out.Items[i].Error = problem // Do not echo invalid/untrusted path metadata.
			continue
		}
		out.Items[i] = Result{File: req.File, Offset: req.Offset}
		demands[i] = req.MaxBytes
		if demands[i] == 0 {
			demands[i] = e.limits.MaxItemBytes
		}
	}
	quotas := allocate(demands, min(e.limits.MaxOutputBytes, e.limits.MaxReadBytes))
	ctx, cancel := context.WithTimeout(ctx, e.limits.Timeout)
	defer cancel()
	var next atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < min(e.limits.Workers, len(requests)); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1) - 1)
				if i >= len(requests) {
					return
				}
				if out.Items[i].Error != nil {
					continue
				}
				out.Items[i] = e.readOne(ctx, scope, requests[i], quotas[i])
			}
		}()
	}
	wg.Wait()
	for _, item := range out.Items {
		out.ReturnedBytes += len(item.Content)
		out.BytesRead += item.BytesRead
		if item.Error != nil {
			out.Failed++
		}
	}
	return out, nil
}

func allocate(demands []int, total int) []int {
	quotas := make([]int, len(demands))
	for total > 0 {
		active := 0
		for i, d := range demands {
			if quotas[i] < d {
				active++
			}
		}
		if active == 0 {
			break
		}
		share := max(1, total/active)
		for i, d := range demands {
			take := min(d-quotas[i], share, total)
			quotas[i] += take
			total -= take
		}
	}
	return quotas
}

func (e *Engine) readOne(ctx context.Context, scope Scope, req Request, quota int) (r Result) {
	r = Result{File: req.File, Offset: req.Offset, AllocatedBytes: quota}
	fail := func(problem *ItemError) Result { clearContent(&r, problem); return r }
	if err := ctx.Err(); err != nil {
		return fail(classify(err))
	}
	if quota == 0 {
		return fail(issue("budget_exhausted", "no bytes allocated; use a smaller batch"))
	}
	h, err := e.source.Open(ctx, scope, req.File)
	if err != nil {
		if h != nil {
			_ = h.Close()
		}
		return fail(classify(err))
	}
	if h == nil {
		return fail(issue("unavailable", "source returned no handle"))
	}
	defer func() {
		closeErr := h.Close()
		if r.Error == nil {
			if err := ctx.Err(); err != nil {
				clearContent(&r, classify(err))
			} else if closeErr != nil {
				clearContent(&r, issue("unavailable", "handle close failed"))
			}
		}
	}()
	before, err := h.Metadata(ctx)
	if err != nil {
		return fail(classify(err))
	}
	if !validMetadata(before) {
		return fail(issue("unavailable", "invalid source metadata"))
	}
	r.SizeBytes, r.Version = before.Size, before.Version
	if req.ExpectedVersion != "" && req.ExpectedVersion != before.Version.Token {
		return fail(issue("stale_version", "file version changed; restart the read"))
	}
	if req.Offset > before.Size {
		return fail(issue("invalid_request", "offset exceeds file size"))
	}
	if err := ctx.Err(); err != nil {
		return fail(classify(err))
	}
	length := int(min(int64(quota), before.Size-req.Offset))
	buf := make([]byte, length)
	var readErr error
	if length > 0 {
		r.BytesRead, readErr = h.ReadAt(buf, req.Offset)
		if r.BytesRead < 0 || r.BytesRead > length {
			r.BytesRead = length
			return fail(issue("unavailable", "source violated ReaderAt contract"))
		}
	}
	if err := ctx.Err(); err != nil {
		return fail(classify(err))
	}
	after, err := h.Metadata(ctx)
	if err != nil {
		return fail(classify(err))
	}
	if !validMetadata(after) || before != after {
		return fail(issue("stale_version", "file changed during read; content discarded"))
	}
	if r.BytesRead != length || readErr != nil && !errors.Is(readErr, io.EOF) {
		return fail(issue("unavailable", "source returned an incomplete read"))
	}
	atEOF := req.Offset+int64(length) == before.Size
	end, problem := textPrefix(buf, atEOF)
	if problem != nil {
		return fail(problem)
	}
	r.Content = string(buf[:end])
	r.EndOffset = req.Offset + int64(end)
	r.EOF = r.EndOffset == before.Size
	if !r.EOF {
		next := r.EndOffset
		r.NextOffset = &next
	}
	return r
}

func validMetadata(m Metadata) bool {
	return m.Size >= 0 && len(m.Version.Token) > 0 && len(m.Version.Token) <= 256 && utf8.ValidString(m.Version.Token) &&
		(m.Version.Strength == "metadata" || m.Version.Strength == "snapshot")
}

// Only a trailing incomplete UTF-8 rune on a non-EOF page may be omitted.
// Invalid bytes and NULs are errors; no replacement characters are synthesized.
func textPrefix(buf []byte, atEOF bool) (int, *ItemError) {
	for i := 0; i < len(buf); {
		if buf[i] == 0 {
			return 0, issue("unsupported_encoding", "NUL-containing data is not supported text")
		}
		if !utf8.FullRune(buf[i:]) {
			if atEOF {
				return 0, issue("unsupported_encoding", "incomplete UTF-8 at end of file")
			}
			if i == 0 {
				return 0, issue("budget_exhausted", "page is too small for one complete UTF-8 character")
			}
			return i, nil
		}
		r, n := utf8.DecodeRune(buf[i:])
		if r == utf8.RuneError && n == 1 {
			return 0, issue("unsupported_encoding", "invalid UTF-8 or offset inside a character")
		}
		i += n
	}
	return len(buf), nil
}

func issue(code, message string) *ItemError { return &ItemError{Code: code, Message: message} }
func clearContent(r *Result, err *ItemError) {
	r.Error, r.Content, r.NextOffset, r.EOF, r.EndOffset = err, "", nil, false, 0
}
func classify(err error) *ItemError {
	switch {
	case errors.Is(err, context.Canceled):
		return issue("cancelled", "request cancelled")
	case errors.Is(err, context.DeadlineExceeded):
		return issue("deadline_exceeded", "request deadline exceeded")
	case errors.Is(err, fs.ErrPermission):
		return issue("denied", "file access denied")
	case errors.Is(err, fs.ErrNotExist):
		return issue("not_found", "file not found")
	default:
		return issue("unavailable", "source operation failed")
	}
}
