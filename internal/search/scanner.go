package search

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/LE-saber/Local-Probe/internal/policy"
	"github.com/LE-saber/Local-Probe/internal/readcore"
)

const scanChunkBytes = 32 << 10

type scanOutcome struct {
	Matches  []Match
	State    scanCursorState
	Complete bool
	Warning  string
}

func (s *Service) scanFile(ctx context.Context, source Source, bound policy.BoundScope, rootID, query string, caseSensitive bool, contextBytes int, state scanCursorState, out *SearchTextResult, remainingMatches, remainingRead, remainingOutput int) (scanOutcome, error) {
	if remainingRead <= 0 {
		return scanOutcome{State: state, Warning: "read_limit"}, nil
	}
	if state.Path == "" || !validRelativePath(state.Path) || remainingMatches <= 0 && remainingMatches != -1 {
		return scanOutcome{State: state, Warning: "page_limit"}, nil
	}
	h, err := source.OpenFile(ctx, bound, readcore.FileRef{RootID: rootID, Path: state.Path})
	if err != nil {
		return scanOutcome{}, mapSourceError(err)
	}
	if h == nil {
		return scanOutcome{}, ErrUnavailable
	}
	defer func() { _ = h.Close() }()
	before, err := h.Metadata(ctx)
	if err != nil {
		return scanOutcome{}, mapSourceError(err)
	}
	if state.Version != "" && state.Version != before.Version.Token {
		return scanOutcome{}, ErrGenerationChanged
	}
	state.Version = before.Version.Token
	if state.Offset < 0 || state.Offset > before.Size || state.Line < 1 || state.LineStartByte < 0 || state.LineStartByte > state.Offset {
		return scanOutcome{}, ErrInvalidCursor
	}
	if state.LastMatchByte == 0 && state.Offset == 0 {
		state.LastMatchByte = -1
	}
	prefix := kmpPrefix([]byte(query), caseSensitive)
	queryBytes := []byte(query)
	if len(queryBytes) == 0 {
		return scanOutcome{}, ErrInvalidRequest
	}
	if state.MatchLen < 0 || state.MatchLen > len(queryBytes) {
		return scanOutcome{}, ErrInvalidCursor
	}
	if !caseSensitive {
		for i, value := range queryBytes {
			queryBytes[i] = foldByte(value)
		}
	}
	matches := make([]Match, 0)
	lineText := make([]byte, 0, minInt(s.limits.MaxLineBytes, 4096))
	lineRecent := make([]byte, 0, minInt(contextBytes+len(queryBytes), 4096))
	lineTruncated := false
	if state.Offset > 0 {
		// The cursor stores only bounded scanner state, not file content. A
		// resumed match therefore gets a local snippet rather than a complete
		// line if the line began before the continuation boundary.
		lineText = nil
	}
	validator := utf8Stream{pending: append([]byte(nil), state.UTF8Carry...)}
	matchLen := state.MatchLen
	lineBytes := state.LineBytes
	lineStart := state.LineStartByte
	lineNumber := state.Line
	lastMatch := state.LastMatchByte
	if lastMatch < -1 {
		lastMatch = -1
	}
	for state.Offset < before.Size {
		if err := ctx.Err(); err != nil {
			state.MatchLen, state.LineBytes, state.LineStartByte, state.Line = matchLen, lineBytes, lineStart, lineNumber
			state.UTF8Carry = append([]byte(nil), validator.pending...)
			return scanOutcome{State: state, Warning: contextWarning(err)}, nil
		}
		if remainingRead <= 0 {
			state.MatchLen, state.LineBytes, state.LineStartByte, state.Line = matchLen, lineBytes, lineStart, lineNumber
			state.UTF8Carry = append([]byte(nil), validator.pending...)
			return scanOutcome{State: state, Warning: "read_limit"}, nil
		}
		chunkSize := minInt(scanChunkBytes, remainingRead)
		if int64(chunkSize) > before.Size-state.Offset {
			chunkSize = int(before.Size - state.Offset)
		}
		if chunkSize <= 0 {
			break
		}
		buf := make([]byte, chunkSize)
		n, readErr := h.ReadAt(buf, state.Offset)
		if n < 0 || n > len(buf) || n != len(buf) || readErr != nil && !errors.Is(readErr, io.EOF) {
			return scanOutcome{}, ErrUnavailable
		}
		out.Coverage.ReadBytes += n
		remainingRead -= n
		for _, value := range buf {
			checkpoint := scanCursorState{Offset: state.Offset, Line: lineNumber, LineStartByte: lineStart, LineBytes: lineBytes, MatchLen: matchLen, LastMatchByte: lastMatch, UTF8Carry: append([]byte(nil), validator.pending...)}
			absolute := state.Offset
			state.Offset++
			if value == 0 {
				return scanOutcome{}, ErrUnsupportedEncoding
			}
			if err := validator.add(value); err != nil {
				return scanOutcome{}, ErrUnsupportedEncoding
			}
			if value == '\n' {
				matchLen = 0
				lineNumber++
				lineStart = state.Offset
				lineBytes = 0
				lineText = lineText[:0]
				lineRecent = lineRecent[:0]
				lineTruncated = false
				lastMatch = -1
				continue
			}
			// Keep the resumable line state bounded even when a file contains a
			// multi-gigabyte line. The exact byte position is tracked separately;
			// once the snippet budget is exceeded we only need the truncation bit.
			if lineBytes <= s.limits.MaxLineBytes {
				lineBytes++
			}
			if len(lineText) < s.limits.MaxLineBytes {
				lineText = append(lineText, value)
			} else {
				lineTruncated = true
			}
			if contextBytes > 0 {
				lineRecent = append(lineRecent, value)
				maxRecent := contextBytes + len(queryBytes)
				if len(lineRecent) > maxRecent {
					lineRecent = lineRecent[len(lineRecent)-maxRecent:]
				}
			}
			candidate := value
			if !caseSensitive {
				candidate = foldByte(value)
			}
			for matchLen > 0 && candidate != queryBytes[matchLen] {
				matchLen = prefix[matchLen-1]
			}
			if candidate == queryBytes[matchLen] {
				matchLen++
			}
			if matchLen == len(queryBytes) {
				start := absolute - int64(len(queryBytes)) + 1
				if start > lastMatch {
					lineValue := string(lineText)
					if strings.HasSuffix(lineValue, "\r") {
						lineValue = strings.TrimSuffix(lineValue, "\r")
					}
					match := Match{Path: state.Path, Line: lineNumber, LineStartByte: lineStart, MatchStartByte: start, MatchEndByte: absolute + 1, LineText: lineValue, LineTruncated: lineTruncated}
					if contextBytes > 0 && len(lineRecent) > 0 {
						match.Context = string(lineRecent)
					} else if lineTruncated || len(lineText) == 0 {
						match.Context = string(queryBytes)
					}
					minimumBytes := estimateMatchBytes(Match{Path: match.Path, Line: match.Line, LineStartByte: match.LineStartByte, MatchStartByte: match.MatchStartByte, MatchEndByte: match.MatchEndByte, LineTruncated: match.LineTruncated})
					if remainingOutput < minimumBytes {
						state = checkpoint
						return scanOutcome{State: state, Matches: matches, Warning: "output_limit"}, nil
					}
					fitted, fits := fitMatch(match, remainingOutput)
					if !fits {
						// A path/metadata record can itself exceed an unusually
						// small output budget. Do not return an unbounded item or
						// repeat the same cursor forever; consume this match and
						// make the omission explicit in warnings.
						appendWarning(&out.Warnings, "match_output_limit")
						lastMatch = start
						matchLen = prefix[matchLen-1]
						continue
					}
					match = fitted
					matchBytes := estimateMatchBytes(match)
					out.Coverage.ReturnedBytes += matchBytes
					if remainingMatches == 0 {
						state.MatchLen, state.LineBytes, state.LineStartByte, state.Line = matchLen, lineBytes, lineStart, lineNumber
						state.LastMatchByte = lastMatch
						state.UTF8Carry = append([]byte(nil), validator.pending...)
						return scanOutcome{State: state, Matches: nil, Warning: "page_limit"}, nil
					}
					// Case folding is ASCII-only by design. Non-ASCII queries remain
					// literal UTF-8 byte matches and are still validated as UTF-8.
					if !caseSensitive && !utf8.Valid(queryBytes) {
						return scanOutcome{}, ErrInvalidRequest
					}
					lastMatch = start
					if remainingMatches > 0 {
						remainingMatches--
					}
					// The caller owns result assembly; stash matches in a local
					// slice below so a file that later fails encoding can be dropped.
					matches = append(matches, match)
				}
				matchLen = prefix[matchLen-1]
			}
			if remainingMatches == 0 {
				state.MatchLen, state.LineBytes, state.LineStartByte, state.Line, state.LastMatchByte = matchLen, lineBytes, lineStart, lineNumber, lastMatch
				state.UTF8Carry = append([]byte(nil), validator.pending...)
				return scanOutcome{State: state, Matches: matches, Warning: "page_limit"}, nil
			}
		}
	}
	if err := validator.finish(); err != nil {
		return scanOutcome{}, ErrUnsupportedEncoding
	}
	after, err := h.Metadata(ctx)
	if err != nil {
		return scanOutcome{}, mapSourceError(err)
	}
	if before != after {
		return scanOutcome{}, ErrGenerationChanged
	}
	state.MatchLen, state.LineBytes, state.LineStartByte, state.Line = matchLen, lineBytes, lineStart, lineNumber
	state.LastMatchByte = lastMatch
	state.UTF8Carry = nil
	return scanOutcome{State: state, Matches: matches, Complete: true}, nil
}

type utf8Stream struct{ pending []byte }

func (v *utf8Stream) add(value byte) error {
	if len(v.pending) == 0 {
		if value < utf8.RuneSelf {
			return nil
		}
		if value < 0xc2 || value > 0xf4 {
			return ErrUnsupportedEncoding
		}
		v.pending = []byte{value}
		return nil
	}
	v.pending = append(v.pending, value)
	if len(v.pending) > 4 {
		return ErrUnsupportedEncoding
	}
	if !utf8.FullRune(v.pending) {
		return nil
	}
	r, n := utf8.DecodeRune(v.pending)
	if r == utf8.RuneError && n == 1 || n != len(v.pending) {
		return ErrUnsupportedEncoding
	}
	v.pending = nil
	return nil
}

func (v *utf8Stream) finish() error {
	if len(v.pending) != 0 {
		return ErrUnsupportedEncoding
	}
	return nil
}

func kmpPrefix(query []byte, caseSensitive bool) []int {
	folded := append([]byte(nil), query...)
	if !caseSensitive {
		for i, value := range folded {
			folded[i] = foldByte(value)
		}
	}
	prefix := make([]int, len(folded))
	for i, j := 1, 0; i < len(folded); i++ {
		for j > 0 && folded[i] != folded[j] {
			j = prefix[j-1]
		}
		if folded[i] == folded[j] {
			j++
		}
		prefix[i] = j
	}
	return prefix
}

func foldByte(value byte) byte {
	if value >= 'A' && value <= 'Z' {
		return value + ('a' - 'A')
	}
	return value
}

func estimateMatchBytes(match Match) int {
	data, err := json.Marshal(match)
	if err != nil {
		// Match fields are validated before reaching the scanner. Keep a
		// conservative fallback in case that invariant changes later.
		return len(match.Path) + len(match.LineText) + len(match.Context) + 256
	}
	return len(data)
}

func fitMatch(match Match, maxBytes int) (Match, bool) {
	match.LineText = validUTF8Prefix(match.LineText)
	match.Context = validUTF8Suffix(match.Context, len(match.Context))
	if estimateMatchBytes(match) <= maxBytes {
		return match, true
	}
	base := match
	base.LineText = ""
	base.Context = ""
	minimum := estimateMatchBytes(base)
	if minimum > maxBytes {
		return Match{}, false
	}
	remaining := maxBytes - minimum
	// Preserve context first because it is intentionally the small, local
	// snippet most likely to include the query when line_text is huge.
	if len(match.Context) > remaining {
		match.Context = validUTF8Suffix(match.Context, remaining)
	}
	remaining -= len(match.Context)
	if len(match.LineText) > remaining {
		match.LineText = validUTF8Suffix(match.LineText, remaining)
		match.LineTruncated = true
	}
	if estimateMatchBytes(match) > maxBytes {
		return Match{}, false
	}
	return match, true
}

func validUTF8Prefix(value string) string {
	data := []byte(value)
	for len(data) > 0 && !utf8.Valid(data) {
		data = data[:len(data)-1]
	}
	return string(data)
}

func validUTF8Suffix(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	data := []byte(value)
	if len(data) > maxBytes {
		data = data[len(data)-maxBytes:]
	}
	for len(data) > 0 && !utf8.Valid(data) {
		data = data[1:]
	}
	return string(data)
}
