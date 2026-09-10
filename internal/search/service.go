package search

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/LE-saber/Local-Probe/internal/policy"
)

func (s *Service) operationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, s.limits.Timeout)
}

func (s *Service) bind(bound policy.BoundScope) (Source, error) {
	if s == nil || s.binder == nil || bound.Validate() != nil {
		return nil, ErrDenied
	}
	source, err := s.binder.BindSearch(bound)
	if err != nil || source == nil {
		return nil, mapSourceError(err)
	}
	return source, nil
}

func (s *Service) base(rootID, path string, bound policy.BoundScope) error {
	if !validText(rootID, 128) || !bound.AllowsRoot(rootID) || !validateRelativePath(path, true) || !bound.AllowsDirectory(rootID, path) {
		return ErrInvalidRequest
	}
	return nil
}

func pageSize(value, defaultValue, maximum int) (int, error) {
	if value == 0 {
		return defaultValue, nil
	}
	if value < 1 || value > maximum {
		return 0, ErrInvalidRequest
	}
	return value, nil
}

func maxEntries(value, maximum int) (int, error) {
	if value == 0 {
		return maximum, nil
	}
	if value < 1 || value > maximum {
		return 0, ErrInvalidRequest
	}
	return value, nil
}

func maxDepth(value, maximum int) (int, error) {
	if value == 0 {
		return maximum, nil
	}
	if value < 0 || value > maximum {
		return 0, ErrInvalidRequest
	}
	return value, nil
}

func outputBudget(value, maximum int) (int, error) {
	if value == 0 {
		return maximum, nil
	}
	if value < 1024 || value > maximum {
		return 0, ErrInvalidRequest
	}
	return value, nil
}

func makeBudget(page, depth, entries, read, output int) Budget {
	return Budget{PageSize: page, MaxDepth: depth, MaxEntries: entries, MaxReadBytes: read, MaxOutputBytes: output}
}

func appendWarning(warnings *[]string, warning string) {
	for _, existing := range *warnings {
		if existing == warning {
			return
		}
	}
	*warnings = append(*warnings, warning)
}

func contextWarning(err error) string {
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "time_limit"
	}
	return ""
}

func entryPathValid(path string, entry DirEntry) bool {
	return entry.Name != "" && strings.IndexByte(entry.Name, '/') < 0 && utf8.ValidString(entry.Name) && validRelativePath(path)
}

func publicEntry(path string, entry DirEntry) Entry {
	out := Entry{Path: path, Name: entry.Name, Type: entry.Type, SizeBytes: entry.SizeBytes}
	if !entry.ModTime.IsZero() {
		out.ModTime = entry.ModTime.UTC().Format(time.RFC3339Nano)
	}
	return out
}

func estimateEntry(entry Entry) int {
	data, err := json.Marshal(entry)
	if err == nil {
		return len(data)
	}
	return len(entry.Path) + len(entry.Name) + len(entry.Type) + len(entry.ModTime) + 128
}

func (s *Service) listCursor(base cursorPayload, frames []cursorFrame) (string, error) {
	base.Frames = frames
	return s.cursor(base)
}

func (s *Service) ListDirectory(ctx context.Context, bound policy.BoundScope, req ListDirectoryRequest) (ListDirectoryResult, error) {
	out := ListDirectoryResult{SchemaVersion: SchemaVersion, RootID: req.RootID, Path: req.Path, Entries: []Entry{}}
	if bound.Validate() != nil {
		if req.Cursor != "" {
			return out, ErrInvalidCursor
		}
		return out, ErrDenied
	}
	if err := s.base(req.RootID, req.Path, bound); err != nil {
		return out, err
	}
	page, err := pageSize(req.PageSize, s.limits.MaxPageSize, s.limits.MaxPageSize)
	if err != nil {
		return out, err
	}
	entriesLimit, err := maxEntries(req.MaxEntries, s.limits.MaxScannedEntries)
	if err != nil {
		return out, err
	}
	out.Budget = makeBudget(page, 0, entriesLimit, 0, s.limits.MaxOutputBytes)
	base := cursorPayload{Operation: "list_directory", ConnectionID: bound.ConnectionID(), ProfileID: bound.ProfileID(), Revision: bound.Revision(), RootID: req.RootID, StartPath: req.Path, PageSize: page, MaxEntries: entriesLimit}
	frames := []cursorFrame{{Path: req.Path}}
	if req.Cursor != "" {
		payload, decodeErr := s.decodeCursor(req.Cursor)
		if decodeErr != nil || payload.Operation != base.Operation || !cursorMatchesScope(payload, bound.ConnectionID(), bound.ProfileID(), bound.Revision(), req.RootID, req.Path) || payload.PageSize != page || payload.MaxEntries != entriesLimit {
			return out, ErrInvalidCursor
		}
		base.ExpiresAt, base.Frames = payload.ExpiresAt, payload.Frames
		frames = payload.Frames
	}
	source, err := s.bind(bound)
	if err != nil {
		return out, err
	}
	opCtx, cancel := s.operationContext(ctx)
	defer cancel()
	w, err := newWalker(opCtx, source, bound, req.RootID, frames, s.limits.DirectoryBatch)
	if err != nil {
		if warning := contextWarning(err); warning != "" {
			appendWarning(&out.Warnings, warning)
			return out, nil
		}
		return out, normalizeOperationError(err)
	}
	defer w.close()
	for {
		if out.Coverage.ScannedEntries >= entriesLimit {
			appendWarning(&out.Warnings, "scan_limit")
			out.Coverage.Complete = false
			if err := w.verifyGenerations(); err != nil {
				return out, normalizeOperationError(err)
			}
			out.Continuation, err = s.listCursor(base, w.framesState())
			if err != nil {
				return out, err
			}
			break
		}
		if warning := contextWarning(opCtx.Err()); warning != "" {
			appendWarning(&out.Warnings, warning)
			out.Coverage.Complete = false
			if err := w.verifyGenerations(); err != nil {
				return out, normalizeOperationError(err)
			}
			out.Continuation, err = s.listCursor(base, w.framesState())
			if err != nil {
				return out, err
			}
			break
		}
		child, entry, _, ok, nextErr := w.next()
		if nextErr != nil {
			if warning := contextWarning(nextErr); warning != "" {
				appendWarning(&out.Warnings, warning)
				out.Coverage.Complete = false
				out.Continuation, err = s.listCursor(base, w.framesState())
				if err != nil {
					return out, err
				}
				break
			}
			return out, normalizeOperationError(nextErr)
		}
		if !ok {
			if err := w.verifyGenerations(); err != nil {
				return out, normalizeOperationError(err)
			}
			out.Coverage.Complete = true
			break
		}
		out.Coverage.ScannedEntries++
		if !entryPathValid(child, entry) {
			out.Coverage.InvalidEncodingEntries++
			appendWarning(&out.Warnings, "invalid_entry_name")
			continue
		}
		if bound.IsDeniedPath(req.RootID, child) {
			out.Coverage.DeniedEntries++
			continue
		}
		if bound.IsIgnoredPath(req.RootID, child) {
			out.Coverage.IgnoredEntries++
			continue
		}
		public := publicEntry(child, entry)
		entryBytes := estimateEntry(public)
		if entryBytes > s.limits.MaxOutputBytes {
			out.Coverage.UnsupportedEntries++
			appendWarning(&out.Warnings, "entry_output_limit")
			continue
		}
		if out.Coverage.ReturnedBytes+entryBytes > s.limits.MaxOutputBytes {
			w.rewindLast()
			appendWarning(&out.Warnings, "output_limit")
			out.Coverage.Complete = false
			out.Continuation, err = s.listCursor(base, w.framesState())
			if err != nil {
				return out, err
			}
			break
		}
		out.Entries = append(out.Entries, public)
		out.Coverage.ReturnedEntries++
		out.Coverage.ReturnedBytes += entryBytes
		if len(out.Entries) >= page {
			appendWarning(&out.Warnings, "page_limit")
			out.Coverage.Complete = false
			if err := w.verifyGenerations(); err != nil {
				return out, normalizeOperationError(err)
			}
			out.Continuation, err = s.listCursor(base, w.framesState())
			if err != nil {
				return out, err
			}
			break
		}
	}
	return out, nil
}

func (s *Service) FindFiles(ctx context.Context, bound policy.BoundScope, req FindFilesRequest) (FindFilesResult, error) {
	out := FindFilesResult{SchemaVersion: SchemaVersion, RootID: req.RootID, Path: req.Path, Pattern: req.Pattern, Entries: []Entry{}}
	if bound.Validate() != nil {
		if req.Cursor != "" {
			return out, ErrInvalidCursor
		}
		return out, ErrDenied
	}
	if err := s.base(req.RootID, req.Path, bound); err != nil || !validateGlob(req.Pattern) {
		return out, ErrInvalidRequest
	}
	page, err := pageSize(req.PageSize, s.limits.MaxPageSize, s.limits.MaxPageSize)
	if err != nil {
		return out, err
	}
	depth, err := maxDepth(req.MaxDepth, s.limits.MaxDepth)
	if err != nil {
		return out, err
	}
	entriesLimit, err := maxEntries(req.MaxEntries, s.limits.MaxScannedEntries)
	if err != nil {
		return out, err
	}
	caseSensitive := defaultCaseSensitive(req.CaseSensitive)
	out.Budget = makeBudget(page, depth, entriesLimit, 0, s.limits.MaxOutputBytes)
	base := cursorPayload{Operation: "find_files", ConnectionID: bound.ConnectionID(), ProfileID: bound.ProfileID(), Revision: bound.Revision(), RootID: req.RootID, StartPath: req.Path, Pattern: req.Pattern, CaseSensitive: caseSensitive, PageSize: page, MaxDepth: depth, MaxEntries: entriesLimit}
	frames := []cursorFrame{{Path: req.Path}}
	if req.Cursor != "" {
		payload, decodeErr := s.decodeCursor(req.Cursor)
		if decodeErr != nil || payload.Operation != base.Operation || !cursorMatchesScope(payload, bound.ConnectionID(), bound.ProfileID(), bound.Revision(), req.RootID, req.Path) || payload.Pattern != req.Pattern || payload.CaseSensitive != caseSensitive || payload.PageSize != page || payload.MaxDepth != depth || payload.MaxEntries != entriesLimit {
			return out, ErrInvalidCursor
		}
		base.ExpiresAt, base.Frames = payload.ExpiresAt, payload.Frames
		frames = payload.Frames
	}
	source, err := s.bind(bound)
	if err != nil {
		return out, err
	}
	opCtx, cancel := s.operationContext(ctx)
	defer cancel()
	w, err := newWalker(opCtx, source, bound, req.RootID, frames, s.limits.DirectoryBatch)
	if err != nil {
		if warning := contextWarning(err); warning != "" {
			appendWarning(&out.Warnings, warning)
			return out, nil
		}
		return out, normalizeOperationError(err)
	}
	defer w.close()
	for {
		if out.Coverage.ScannedEntries >= entriesLimit {
			appendWarning(&out.Warnings, "scan_limit")
			out.Coverage.Complete = false
			if err := w.verifyGenerations(); err != nil {
				return out, normalizeOperationError(err)
			}
			out.Continuation, err = s.listCursor(base, w.framesState())
			if err != nil {
				return out, err
			}
			break
		}
		if warning := contextWarning(opCtx.Err()); warning != "" {
			appendWarning(&out.Warnings, warning)
			out.Coverage.Complete = false
			out.Continuation, err = s.listCursor(base, w.framesState())
			if err != nil {
				return out, err
			}
			break
		}
		child, entry, depthAt, ok, nextErr := w.next()
		if nextErr != nil {
			if warning := contextWarning(nextErr); warning != "" {
				appendWarning(&out.Warnings, warning)
				out.Coverage.Complete = false
				out.Continuation, err = s.listCursor(base, w.framesState())
				if err != nil {
					return out, err
				}
				break
			}
			return out, normalizeOperationError(nextErr)
		}
		if !ok {
			if err := w.verifyGenerations(); err != nil {
				return out, normalizeOperationError(err)
			}
			out.Coverage.Complete = out.Coverage.DepthLimitedEntries == 0
			break
		}
		out.Coverage.ScannedEntries++
		if !entryPathValid(child, entry) {
			out.Coverage.InvalidEncodingEntries++
			appendWarning(&out.Warnings, "invalid_entry_name")
			continue
		}
		if bound.IsDeniedPath(req.RootID, child) {
			out.Coverage.DeniedEntries++
			continue
		}
		if bound.IsIgnoredPath(req.RootID, child) {
			out.Coverage.IgnoredEntries++
			continue
		}
		if entry.Type == EntryDirectory {
			if depthAt >= depth {
				out.Coverage.DepthLimitedEntries++
				appendWarning(&out.Warnings, "depth_limit")
				continue
			}
			if pushErr := w.push(child); pushErr != nil {
				if errors.Is(pushErr, ErrGenerationChanged) {
					return out, ErrGenerationChanged
				}
				out.Coverage.UnsupportedEntries++
				appendWarning(&out.Warnings, "directory_unavailable")
			}
			continue
		}
		if entry.Type != EntryRegular {
			out.Coverage.UnsupportedEntries++
			appendWarning(&out.Warnings, "unsupported_entry")
			continue
		}
		if !matchGlob(req.Pattern, child, caseSensitive) {
			continue
		}
		public := publicEntry(child, entry)
		entryBytes := estimateEntry(public)
		if entryBytes > s.limits.MaxOutputBytes {
			out.Coverage.UnsupportedEntries++
			appendWarning(&out.Warnings, "entry_output_limit")
			continue
		}
		if out.Coverage.ReturnedBytes+entryBytes > s.limits.MaxOutputBytes {
			w.rewindLast()
			appendWarning(&out.Warnings, "output_limit")
			out.Coverage.Complete = false
			out.Continuation, err = s.listCursor(base, w.framesState())
			if err != nil {
				return out, err
			}
			break
		}
		out.Entries = append(out.Entries, public)
		out.Coverage.ReturnedEntries++
		out.Coverage.ReturnedBytes += entryBytes
		if len(out.Entries) >= page {
			appendWarning(&out.Warnings, "page_limit")
			out.Coverage.Complete = false
			if err := w.verifyGenerations(); err != nil {
				return out, normalizeOperationError(err)
			}
			out.Continuation, err = s.listCursor(base, w.framesState())
			if err != nil {
				return out, err
			}
			break
		}
	}
	return out, nil
}

func (s *Service) SearchText(ctx context.Context, bound policy.BoundScope, req SearchTextRequest) (SearchTextResult, error) {
	out := SearchTextResult{SchemaVersion: SchemaVersion, RootID: req.RootID, Path: req.Path, Query: req.Query, Matches: []Match{}}
	if bound.Validate() != nil {
		if req.Cursor != "" {
			return out, ErrInvalidCursor
		}
		return out, ErrDenied
	}
	if err := s.base(req.RootID, req.Path, bound); err != nil || !validText(req.Query, 4096) || strings.ContainsAny(req.Query, "\r\n") {
		return out, ErrInvalidRequest
	}
	globs := append([]string(nil), req.Globs...)
	if len(globs) > 32 {
		return out, ErrInvalidRequest
	}
	for _, glob := range globs {
		if !validateGlob(glob) {
			return out, ErrInvalidRequest
		}
	}
	page, err := pageSize(req.PageSize, s.limits.MaxPageSize, s.limits.MaxPageSize)
	if err != nil {
		return out, err
	}
	depth, err := maxDepth(req.MaxDepth, s.limits.MaxDepth)
	if err != nil {
		return out, err
	}
	entriesLimit, err := maxEntries(req.MaxEntries, s.limits.MaxScannedEntries)
	if err != nil {
		return out, err
	}
	readLimit, err := outputBudget(req.MaxReadBytes, s.limits.MaxReadBytes)
	if err != nil {
		return out, err
	}
	contextBytes := req.ContextBytes
	if contextBytes == 0 {
		contextBytes = s.limits.MaxContextBytes
	}
	if contextBytes < 0 || contextBytes > s.limits.MaxContextBytes {
		return out, ErrInvalidRequest
	}
	caseSensitive := defaultCaseSensitive(req.CaseSensitive)
	out.Budget = makeBudget(page, depth, entriesLimit, readLimit, s.limits.MaxOutputBytes)
	base := cursorPayload{Operation: "search_text", ConnectionID: bound.ConnectionID(), ProfileID: bound.ProfileID(), Revision: bound.Revision(), RootID: req.RootID, StartPath: req.Path, Query: req.Query, Globs: globs, CaseSensitive: caseSensitive, PageSize: page, MaxDepth: depth, MaxEntries: entriesLimit, MaxReadBytes: readLimit, ContextBytes: contextBytes}
	frames := []cursorFrame{{Path: req.Path}}
	var pending *scanCursorState
	if req.Cursor != "" {
		payload, decodeErr := s.decodeCursor(req.Cursor)
		if decodeErr != nil || payload.Operation != base.Operation || !cursorMatchesScope(payload, bound.ConnectionID(), bound.ProfileID(), bound.Revision(), req.RootID, req.Path) || payload.Query != req.Query || !equalStrings(payload.Globs, globs) || payload.CaseSensitive != caseSensitive || payload.PageSize != page || payload.MaxDepth != depth || payload.MaxEntries != entriesLimit || payload.MaxReadBytes != readLimit || payload.ContextBytes != contextBytes {
			return out, ErrInvalidCursor
		}
		base.ExpiresAt, base.Frames, pending = payload.ExpiresAt, payload.Frames, payload.Pending
		frames = payload.Frames
	}
	source, err := s.bind(bound)
	if err != nil {
		return out, err
	}
	opCtx, cancel := s.operationContext(ctx)
	defer cancel()
	w, err := newWalker(opCtx, source, bound, req.RootID, frames, s.limits.DirectoryBatch)
	if err != nil {
		if warning := contextWarning(err); warning != "" {
			appendWarning(&out.Warnings, warning)
			return out, nil
		}
		return out, normalizeOperationError(err)
	}
	defer w.close()
	for {
		if warning := contextWarning(opCtx.Err()); warning != "" {
			appendWarning(&out.Warnings, warning)
			out.Coverage.Complete = false
			base.Pending = pending
			out.Continuation, err = s.listCursor(base, w.framesState())
			if err != nil {
				return out, err
			}
			break
		}
		if pending != nil {
			scan, scanErr := s.scanFile(opCtx, source, bound, req.RootID, req.Query, caseSensitive, contextBytes, *pending, &out, page-len(out.Matches), readLimit-out.Coverage.ReadBytes, s.limits.MaxOutputBytes-out.Coverage.ReturnedBytes)
			if scanErr != nil {
				if errors.Is(scanErr, ErrUnsupportedEncoding) {
					out.Coverage.UnsupportedEntries++
					appendWarning(&out.Warnings, "unsupported_encoding")
					pending = nil
					base.Pending = nil
					continue
				}
				return out, normalizeSearchScanError(scanErr, &out)
			}
			if len(scan.Matches) != 0 {
				out.Matches = append(out.Matches, scan.Matches...)
				out.Coverage.ReturnedEntries += len(scan.Matches)
			}
			if !scan.Complete {
				appendWarning(&out.Warnings, scan.Warning)
				out.Coverage.Complete = false
				pending = &scan.State
				base.Pending = pending
				if err := w.verifyGenerations(); err != nil {
					return out, normalizeOperationError(err)
				}
				out.Continuation, err = s.listCursor(base, w.framesState())
				if err != nil {
					return out, err
				}
				break
			}
			pending = nil
			base.Pending = nil
			continue
		}
		if out.Coverage.ScannedEntries >= entriesLimit {
			appendWarning(&out.Warnings, "scan_limit")
			out.Coverage.Complete = false
			base.Pending = nil
			out.Continuation, err = s.listCursor(base, w.framesState())
			if err != nil {
				return out, err
			}
			break
		}
		child, entry, depthAt, ok, nextErr := w.next()
		if nextErr != nil {
			if warning := contextWarning(nextErr); warning != "" {
				appendWarning(&out.Warnings, warning)
				out.Coverage.Complete = false
				base.Pending = pending
				out.Continuation, err = s.listCursor(base, w.framesState())
				if err != nil {
					return out, err
				}
				break
			}
			return out, normalizeOperationError(nextErr)
		}
		if !ok {
			if err := w.verifyGenerations(); err != nil {
				return out, normalizeOperationError(err)
			}
			out.Coverage.Complete = out.Coverage.DepthLimitedEntries == 0 && out.Coverage.UnsupportedEntries == 0
			break
		}
		out.Coverage.ScannedEntries++
		if !entryPathValid(child, entry) {
			out.Coverage.InvalidEncodingEntries++
			appendWarning(&out.Warnings, "invalid_entry_name")
			continue
		}
		if bound.IsDeniedPath(req.RootID, child) {
			out.Coverage.DeniedEntries++
			continue
		}
		if bound.IsIgnoredPath(req.RootID, child) {
			out.Coverage.IgnoredEntries++
			continue
		}
		if entry.Type == EntryDirectory {
			if depthAt >= depth {
				out.Coverage.DepthLimitedEntries++
				appendWarning(&out.Warnings, "depth_limit")
				continue
			}
			if pushErr := w.push(child); pushErr != nil {
				if errors.Is(pushErr, ErrGenerationChanged) {
					return out, ErrGenerationChanged
				}
				out.Coverage.UnsupportedEntries++
				appendWarning(&out.Warnings, "directory_unavailable")
			}
			continue
		}
		if entry.Type != EntryRegular || !globMatches(globs, child, caseSensitive) {
			if entry.Type != EntryRegular {
				out.Coverage.UnsupportedEntries++
				appendWarning(&out.Warnings, "unsupported_entry")
			}
			continue
		}
		state := scanCursorState{Path: child, Offset: 0, Line: 1, LineStartByte: 0, LastMatchByte: -1}
		scan, scanErr := s.scanFile(opCtx, source, bound, req.RootID, req.Query, caseSensitive, contextBytes, state, &out, page-len(out.Matches), readLimit-out.Coverage.ReadBytes, s.limits.MaxOutputBytes-out.Coverage.ReturnedBytes)
		if scanErr != nil {
			if errors.Is(scanErr, ErrUnsupportedEncoding) {
				out.Coverage.UnsupportedEntries++
				appendWarning(&out.Warnings, "unsupported_encoding")
				continue
			}
			return out, normalizeSearchScanError(scanErr, &out)
		}
		out.Matches = append(out.Matches, scan.Matches...)
		out.Coverage.ReturnedEntries += len(scan.Matches)
		if !scan.Complete {
			appendWarning(&out.Warnings, scan.Warning)
			out.Coverage.Complete = false
			pending = &scan.State
			base.Pending = pending
			if err := w.verifyGenerations(); err != nil {
				return out, normalizeOperationError(err)
			}
			out.Continuation, err = s.listCursor(base, w.framesState())
			if err != nil {
				return out, err
			}
			break
		}
		if len(out.Matches) >= page {
			appendWarning(&out.Warnings, "page_limit")
			out.Coverage.Complete = false
			base.Pending = nil
			if err := w.verifyGenerations(); err != nil {
				return out, normalizeOperationError(err)
			}
			out.Continuation, err = s.listCursor(base, w.framesState())
			if err != nil {
				return out, err
			}
			break
		}
	}
	return out, nil
}

func globMatches(globs []string, value string, caseSensitive bool) bool {
	if len(globs) == 0 {
		return true
	}
	for _, glob := range globs {
		if matchGlob(glob, value, caseSensitive) || matchGlob(glob, lastPathComponent(value), caseSensitive) {
			return true
		}
	}
	return false
}

func lastPathComponent(value string) string {
	if idx := strings.LastIndexByte(value, '/'); idx >= 0 {
		return value[idx+1:]
	}
	return value
}

func normalizeOperationError(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, ErrInvalidRequest) || errors.Is(err, ErrInvalidCursor) || errors.Is(err, ErrGenerationChanged) || errors.Is(err, ErrDenied) {
		return err
	}
	return ErrUnavailable
}

var ErrUnsupportedEncoding = errors.New("unsupported text encoding")

func normalizeSearchScanError(err error, out *SearchTextResult) error {
	if errors.Is(err, ErrUnsupportedEncoding) {
		out.Coverage.UnsupportedEntries++
		appendWarning(&out.Warnings, "unsupported_encoding")
		return ErrUnsupportedEncoding
	}
	if errors.Is(err, context.Canceled) {
		appendWarning(&out.Warnings, "cancelled")
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		appendWarning(&out.Warnings, "time_limit")
		return context.DeadlineExceeded
	}
	if errors.Is(err, ErrGenerationChanged) || errors.Is(err, ErrInvalidCursor) || errors.Is(err, ErrDenied) {
		return err
	}
	return ErrUnavailable
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
