package search

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"strings"
	"unicode/utf8"

	"github.com/LE-saber/Local-Probe/internal/policy"
)

type walkFrame struct {
	state cursorFrame
	dir   Directory
	batch []DirEntry
	index int
}

type walker struct {
	ctx       context.Context
	source    Source
	bound     policy.BoundScope
	rootID    string
	batchSize int
	frames    []walkFrame
	last      bool
}

func newWalker(ctx context.Context, source Source, bound policy.BoundScope, rootID string, frames []cursorFrame, batchSize int) (*walker, error) {
	if ctx == nil || source == nil || !bound.AllowsRoot(rootID) || batchSize < 1 {
		return nil, ErrDenied
	}
	if len(frames) == 0 {
		frames = []cursorFrame{{Path: ""}}
	}
	w := &walker{ctx: ctx, source: source, bound: bound, rootID: rootID, batchSize: batchSize}
	for _, state := range frames {
		frame, done, err := w.openFrame(state)
		if err != nil {
			w.close()
			return nil, err
		}
		if done {
			// A completed deepest frame is normal when a cursor was issued
			// immediately after its last item. Keep its parent stack so the
			// next call can continue with the parent's following sibling.
			// If the root frame itself is complete, an empty stack represents
			// the finished traversal.
			if len(w.frames) == 0 {
				return w, nil
			}
			break
		}
		w.frames = append(w.frames, frame)
	}
	return w, nil
}

func (w *walker) openFrame(state cursorFrame) (walkFrame, bool, error) {
	if err := w.ctx.Err(); err != nil {
		return walkFrame{}, false, err
	}
	if state.Path != "" && !validRelativePath(state.Path) || !w.bound.AllowsDirectory(w.rootID, state.Path) {
		return walkFrame{}, false, ErrDenied
	}
	dir, err := w.source.OpenDirectory(w.ctx, w.bound, w.rootID, state.Path)
	if err != nil {
		return walkFrame{}, false, mapSourceError(err)
	}
	gen, err := dir.Generation()
	if err != nil {
		_ = dir.Close()
		return walkFrame{}, false, ErrUnavailable
	}
	if state.Generation != "" && state.Generation != gen {
		_ = dir.Close()
		return walkFrame{}, false, ErrGenerationChanged
	}
	state.Generation = gen
	frame := walkFrame{state: state, dir: dir}
	remaining := state.Offset
	for remaining > 0 {
		entries, readErr := frame.dir.ReadDir(w.batchSize)
		if len(entries) != 0 {
			if remaining < len(entries) {
				frame.batch = entries
				frame.index = remaining
				remaining = 0
				break
			}
			remaining -= len(entries)
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) && len(entries) == 0 {
				_ = dir.Close()
				if remaining > 0 {
					return walkFrame{}, false, ErrGenerationChanged
				}
				return walkFrame{}, true, nil
			}
			if !errors.Is(readErr, io.EOF) {
				_ = dir.Close()
				return walkFrame{}, false, ErrUnavailable
			}
		}
	}
	return frame, false, nil
}

func (w *walker) next() (path string, entry DirEntry, depth int, ok bool, err error) {
	w.last = false
	for len(w.frames) > 0 {
		if err := w.ctx.Err(); err != nil {
			return "", DirEntry{}, 0, false, err
		}
		idx := len(w.frames) - 1
		frame := &w.frames[idx]
		if frame.index >= len(frame.batch) {
			entries, readErr := frame.dir.ReadDir(w.batchSize)
			if len(entries) != 0 {
				frame.batch = entries
				frame.index = 0
			} else if readErr != nil && !errors.Is(readErr, io.EOF) {
				return "", DirEntry{}, 0, false, ErrUnavailable
			} else if len(entries) == 0 {
				_ = frame.dir.Close()
				w.frames = w.frames[:idx]
				continue
			}
		}
		entry = frame.batch[frame.index]
		frame.index++
		frame.state.Offset++
		if entry.Name == "" || strings.Contains(entry.Name, "/") || !utf8.ValidString(entry.Name) {
			// The caller counts malformed names as an uncovered entry; the
			// cursor still advances so a hostile directory cannot loop forever.
			w.last = true
			return "", entry, idx, true, nil
		}
		if frame.state.Path == "" {
			path = entry.Name
		} else {
			path = frame.state.Path + "/" + entry.Name
		}
		w.last = true
		return path, entry, idx, true, nil
	}
	return "", DirEntry{}, 0, false, nil
}

func (w *walker) push(path string) error {
	frame, done, err := w.openFrame(cursorFrame{Path: path})
	if err != nil {
		return err
	}
	if done {
		return nil
	}
	w.frames = append(w.frames, frame)
	return nil
}

func (w *walker) rewindLast() {
	if !w.last || len(w.frames) == 0 {
		return
	}
	frame := &w.frames[len(w.frames)-1]
	if frame.state.Offset > 0 {
		frame.state.Offset--
	}
	if frame.index > 0 {
		frame.index--
	}
	w.last = false
}

func (w *walker) framesState() []cursorFrame {
	out := make([]cursorFrame, len(w.frames))
	for i, frame := range w.frames {
		out[i] = frame.state
	}
	return out
}

func (w *walker) verifyGenerations() error {
	for _, frame := range w.frames {
		gen, err := frame.dir.Generation()
		if err != nil || gen != frame.state.Generation {
			return ErrGenerationChanged
		}
	}
	return nil
}

func (w *walker) close() {
	for i := len(w.frames) - 1; i >= 0; i-- {
		_ = w.frames[i].dir.Close()
	}
	w.frames = nil
}

func mapSourceError(err error) error {
	if err == nil {
		return ErrUnavailable
	}
	if errors.Is(err, ErrDenied) {
		return ErrDenied
	}
	if errors.Is(err, ErrGenerationChanged) {
		return ErrGenerationChanged
	}
	if errors.Is(err, fs.ErrPermission) {
		return ErrDenied
	}
	if errors.Is(err, fs.ErrNotExist) {
		return ErrUnavailable
	}
	return ErrUnavailable
}
