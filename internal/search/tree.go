package search

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/LE-saber/Local-Probe/internal/policy"
)

// TreeDirectory returns a flat, depth-first pre-order view of one authorized
// directory. It is deliberately stateless: path selects the configured root
// range for this call and a signed cursor carries only bounded traversal state.
// It never changes a process working directory or invokes an external tree
// command.
func (s *Service) TreeDirectory(ctx context.Context, bound policy.BoundScope, req TreeDirectoryRequest) (TreeDirectoryResult, error) {
	out := TreeDirectoryResult{SchemaVersion: SchemaVersion, RootID: req.RootID, Path: req.Path, Entries: []TreeEntry{}}
	if bound.Validate() != nil {
		if req.Cursor != "" {
			return out, ErrInvalidCursor
		}
		return out, ErrDenied
	}
	if err := s.base(req.RootID, req.Path, bound); err != nil {
		return out, err
	}
	depth, err := maxDepth(req.MaxDepth, s.limits.MaxDepth)
	if err != nil {
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
	out.Budget = makeBudget(page, depth, entriesLimit, 0, s.limits.MaxOutputBytes)

	base := cursorPayload{
		Operation:      "tree_directory",
		ConnectionID:   bound.ConnectionID(),
		ProfileID:      bound.ProfileID(),
		Revision:       bound.Revision(),
		RootID:         req.RootID,
		StartPath:      req.Path,
		PageSize:       page,
		MaxDepth:       depth,
		MaxEntries:     entriesLimit,
		MaxOutputBytes: s.limits.MaxOutputBytes,
		RootEmitted:    true,
	}
	frames := []cursorFrame{{Path: req.Path}}
	rootEmitted := false
	if req.Cursor != "" {
		payload, decodeErr := s.decodeCursor(req.Cursor)
		if decodeErr != nil || payload.Operation != base.Operation ||
			!cursorMatchesScope(payload, bound.ConnectionID(), bound.ProfileID(), bound.Revision(), req.RootID, req.Path) ||
			payload.PageSize != page || payload.MaxDepth != depth || payload.MaxEntries != entriesLimit ||
			payload.MaxOutputBytes != base.MaxOutputBytes || !payload.RootEmitted {
			return out, ErrInvalidCursor
		}
		base.ExpiresAt, base.Frames, base.RootEmitted = payload.ExpiresAt, payload.Frames, payload.RootEmitted
		frames = payload.Frames
		rootEmitted = payload.RootEmitted
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

	if !rootEmitted {
		root := TreeEntry{Path: req.Path, Name: treeRootName(req.Path), Type: EntryDirectory, Depth: 0}
		rootBytes := estimateTreeEntry(root)
		if rootBytes > s.limits.MaxOutputBytes {
			return out, ErrInvalidRequest
		}
		out.Entries = append(out.Entries, root)
		out.Coverage.ReturnedEntries++
		out.Coverage.ReturnedBytes += rootBytes
		rootEmitted = true
		if len(out.Entries) >= page {
			appendWarning(&out.Warnings, "page_limit")
			out.Coverage.Complete = false
			out.Continuation, err = s.treeCursor(base, w.framesState())
			if err != nil {
				return out, err
			}
			return out, nil
		}
	}

	for {
		if out.Coverage.ScannedEntries >= entriesLimit {
			appendWarning(&out.Warnings, "scan_limit")
			out.Coverage.Complete = false
			if err := w.verifyGenerations(); err != nil {
				return out, normalizeOperationError(err)
			}
			out.Continuation, err = s.treeCursor(base, w.framesState())
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
			out.Continuation, err = s.treeCursor(base, w.framesState())
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
				out.Continuation, err = s.treeCursor(base, w.framesState())
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
			out.Coverage.Complete = out.Coverage.DepthLimitedEntries == 0 &&
				out.Coverage.UnsupportedEntries == 0 && out.Coverage.InvalidEncodingEntries == 0
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

		public := publicTreeEntry(child, entry, depthAt+1)
		entryBytes := estimateTreeEntry(public)
		if entryBytes > s.limits.MaxOutputBytes {
			out.Coverage.UnsupportedEntries++
			appendWarning(&out.Warnings, "entry_output_limit")
			continue
		}
		if out.Coverage.ReturnedBytes+entryBytes > s.limits.MaxOutputBytes {
			w.rewindLast()
			appendWarning(&out.Warnings, "output_limit")
			out.Coverage.Complete = false
			if err := w.verifyGenerations(); err != nil {
				return out, normalizeOperationError(err)
			}
			out.Continuation, err = s.treeCursor(base, w.framesState())
			if err != nil {
				return out, err
			}
			break
		}

		out.Entries = append(out.Entries, public)
		out.Coverage.ReturnedEntries++
		out.Coverage.ReturnedBytes += entryBytes

		switch entry.Type {
		case EntryDirectory:
			if public.Depth >= depth {
				out.Coverage.DepthLimitedEntries++
				appendWarning(&out.Warnings, "depth_limit")
			} else if pushErr := w.push(child); pushErr != nil {
				if errors.Is(pushErr, ErrGenerationChanged) {
					return out, ErrGenerationChanged
				}
				out.Coverage.UnsupportedEntries++
				appendWarning(&out.Warnings, "directory_unavailable")
			}
		case EntryRegular:
			// Regular files are leaves and need no additional filesystem call.
		default:
			// Symlink/reparse entries are visible as metadata but never traversed.
			out.Coverage.UnsupportedEntries++
			appendWarning(&out.Warnings, "unsupported_entry")
		}

		if len(out.Entries) >= page {
			appendWarning(&out.Warnings, "page_limit")
			out.Coverage.Complete = false
			if err := w.verifyGenerations(); err != nil {
				return out, normalizeOperationError(err)
			}
			out.Continuation, err = s.treeCursor(base, w.framesState())
			if err != nil {
				return out, err
			}
			break
		}
	}
	return out, nil
}

func (s *Service) treeCursor(base cursorPayload, frames []cursorFrame) (string, error) {
	base.RootEmitted = true
	base.Frames = frames
	return s.cursor(base)
}

func treeRootName(path string) string {
	if path == "" {
		return "."
	}
	return lastPathComponent(path)
}

func publicTreeEntry(path string, entry DirEntry, depth int) TreeEntry {
	public := TreeEntry{Path: path, Name: entry.Name, Type: entry.Type, Depth: depth, SizeBytes: entry.SizeBytes}
	if !entry.ModTime.IsZero() {
		public.ModTime = entry.ModTime.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	}
	return public
}

func estimateTreeEntry(entry TreeEntry) int {
	data, err := json.Marshal(entry)
	if err == nil {
		return len(data)
	}
	return len(entry.Path) + len(entry.Name) + len(entry.Type) + len(entry.ModTime) + 128
}
