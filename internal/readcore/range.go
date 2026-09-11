package readcore

import (
	"context"
	"errors"
	"io"
)

type normalizedRange struct {
	kind         RangeKind
	startLine    int64
	maxLines     int
	tailLines    int
	maxScanBytes int
}

func normalizeRange(r Request, limits Limits) normalizedRange {
	kind := r.Range.Kind
	if kind == "" {
		kind = RangeBytes
	}
	maxScan := r.Range.MaxScanBytes
	if maxScan == 0 {
		maxScan = limits.MaxScanBytes
	}
	return normalizedRange{
		kind:         kind,
		startLine:    r.Range.StartLine,
		maxLines:     r.Range.MaxLines,
		tailLines:    r.Range.TailLines,
		maxScanBytes: maxScan,
	}
}

func outputDemand(r Request, limits Limits) int {
	if r.MaxBytes == 0 {
		return limits.MaxItemBytes
	}
	return r.MaxBytes
}

func scanDemand(r Request, limits Limits) int {
	nr := normalizeRange(r, limits)
	if nr.kind == RangeBytes {
		return outputDemand(r, limits)
	}
	return minInt(nr.maxScanBytes, limits.MaxReadBytes)
}

type rangeData struct {
	content    []byte
	offset     int64
	endOffset  int64
	startLine  int64
	endLine    int64
	bytesRead  int
	scanned    int
	eof        bool
	complete   bool
	nextOffset *int64
}

func readBytesRange(ctx context.Context, h Handle, metadata Metadata, req Request, quota int) (rangeData, *ItemError) {
	data := rangeData{offset: req.Offset}
	if req.Offset > metadata.Size {
		return data, issue("invalid_request", "offset exceeds file size")
	}
	length := int(minInt64(int64(quota), metadata.Size-req.Offset))
	buf := make([]byte, length)
	var readErr error
	if length > 0 {
		data.bytesRead, readErr = readAt(ctx, h, buf, req.Offset)
		if errors.Is(readErr, context.Canceled) || errors.Is(readErr, context.DeadlineExceeded) {
			return data, classify(readErr)
		}
		if data.bytesRead < 0 || data.bytesRead > length {
			if data.bytesRead < 0 {
				data.bytesRead = 0
			}
			if data.bytesRead > length {
				data.bytesRead = length
			}
			return data, issue("unavailable", "source violated ReaderAt contract")
		}
	}
	data.scanned = data.bytesRead
	if data.bytesRead != length || readErr != nil && !errors.Is(readErr, io.EOF) {
		return data, issue("unavailable", "source returned an incomplete read")
	}
	atEOF := req.Offset+int64(length) == metadata.Size
	end, problem := textPrefix(buf, atEOF)
	if problem != nil {
		return data, problem
	}
	data.content = buf[:end]
	data.endOffset = req.Offset + int64(end)
	data.eof = data.endOffset == metadata.Size
	data.complete = data.eof
	if !data.eof {
		next := data.endOffset
		data.nextOffset = &next
	}
	return data, nil
}

func readLinesRange(ctx context.Context, h Handle, metadata Metadata, r normalizedRange, outputQuota, scanQuota, blockSize int) (rangeData, *ItemError) {
	data := rangeData{complete: false}
	if outputQuota <= 0 || scanQuota <= 0 {
		return data, issue("budget_exhausted", "line read budget is exhausted")
	}
	if metadata.Size == 0 {
		data.complete, data.eof = true, true
		return data, nil
	}
	if blockSize <= 0 {
		blockSize = 32 << 10
	}
	scanLimit := minInt(r.maxScanBytes, scanQuota)
	lineNumber := int64(1)
	lineStart := int64(0)
	lineCount := 0
	var content []byte
	var startOffset int64
	var endOffset int64
	started := false
	position := int64(0)

	for position < metadata.Size {
		if err := ctx.Err(); err != nil {
			return data, classify(err)
		}
		if data.scanned >= scanLimit {
			return data, issue("budget_exhausted", "line scan budget exhausted before the requested range")
		}
		chunkSize := minInt(blockSize, scanLimit-data.scanned)
		if int64(chunkSize) > metadata.Size-position {
			chunkSize = int(metadata.Size - position)
		}
		if chunkSize <= 0 {
			break
		}
		buf := make([]byte, chunkSize)
		n, readErr := readAt(ctx, h, buf, position)
		if errors.Is(readErr, context.Canceled) || errors.Is(readErr, context.DeadlineExceeded) {
			return data, classify(readErr)
		}
		if n < 0 {
			n = 0
		}
		if n > len(buf) {
			n = len(buf)
		}
		data.bytesRead += n
		data.scanned += n
		if n < 0 || n > len(buf) || n != len(buf) || readErr != nil && !errors.Is(readErr, io.EOF) {
			return data, issue("unavailable", "source returned an incomplete read")
		}
		for i, value := range buf {
			absolute := position + int64(i)
			if value == 0 {
				return data, issue("unsupported_encoding", "NUL-containing data is not supported text")
			}
			inRange := lineNumber >= r.startLine && lineNumber < r.startLine+int64(r.maxLines)
			if inRange {
				if !started {
					started = true
					startOffset = lineStart
				}
				if len(content) == outputQuota {
					return data, issue("budget_exhausted", "line output budget exhausted before the requested range")
				}
				content = append(content, value)
			}
			if value == '\n' {
				if inRange {
					lineCount++
					endOffset = absolute + 1
					if lineCount == r.maxLines {
						end, problem := textPrefix(content, endOffset == metadata.Size)
						if problem != nil {
							return data, problem
						}
						data.content = content[:end]
						data.offset, data.endOffset = startOffset, endOffset
						data.startLine, data.endLine = r.startLine, r.startLine+int64(lineCount)-1
						data.eof, data.complete = endOffset == metadata.Size, true
						return data, nil
					}
				}
				lineNumber++
				lineStart = absolute + 1
			}
		}
		position += int64(n)
		if readErr != nil && errors.Is(readErr, io.EOF) {
			break
		}
	}

	if position < metadata.Size {
		return data, issue("budget_exhausted", "line scan budget exhausted before the requested range")
	}
	// A final unterminated record is a line. A file ending in '\n' has no
	// phantom empty line after it.
	if lineStart < metadata.Size && lineNumber >= r.startLine && lineNumber < r.startLine+int64(r.maxLines) {
		if !started {
			started = true
			startOffset = lineStart
		}
		lineCount++
		endOffset = metadata.Size
	}
	if !started {
		data.complete, data.eof = true, true
		return data, nil
	}
	end, problem := textPrefix(content, true)
	if problem != nil {
		return data, problem
	}
	data.content = content[:end]
	data.offset, data.endOffset = startOffset, endOffset
	data.startLine, data.endLine = r.startLine, r.startLine+int64(lineCount)-1
	data.eof, data.complete = endOffset == metadata.Size, true
	return data, nil
}

func readTailRange(ctx context.Context, h Handle, metadata Metadata, r normalizedRange, outputQuota, scanQuota, blockSize int) (rangeData, *ItemError) {
	data := rangeData{complete: false, offset: metadata.Size, endOffset: metadata.Size}
	if outputQuota <= 0 || scanQuota <= 0 {
		return data, issue("budget_exhausted", "tail read budget is exhausted")
	}
	if metadata.Size == 0 {
		data.complete, data.eof = true, true
		return data, nil
	}
	if blockSize <= 0 {
		blockSize = 32 << 10
	}
	scanLimit := minInt(r.maxScanBytes, scanQuota)
	position := metadata.Size
	pieces := make([]tailPiece, 0, 8)
	newlineCount := 0
	cutOffset := int64(0)
	foundCut := false
	// The target differs for a file ending in a newline: the trailing newline
	// terminates a record and the previous delimiter identifies its start.
	target := r.tailLines
	endsWithNewline := false
	for position > 0 {
		if err := ctx.Err(); err != nil {
			return data, classify(err)
		}
		if data.scanned >= scanLimit {
			return data, issue("budget_exhausted", "tail scan budget exhausted before the requested records were found")
		}
		chunkSize := minInt(blockSize, scanLimit-data.scanned)
		if int64(chunkSize) > position {
			chunkSize = int(position)
		}
		start := position - int64(chunkSize)
		buf := make([]byte, chunkSize)
		n, readErr := readAt(ctx, h, buf, start)
		if errors.Is(readErr, context.Canceled) || errors.Is(readErr, context.DeadlineExceeded) {
			return data, classify(readErr)
		}
		if n < 0 {
			n = 0
		}
		if n > len(buf) {
			n = len(buf)
		}
		data.bytesRead += n
		data.scanned += n
		if n < 0 || n > len(buf) || n != len(buf) || readErr != nil && !errors.Is(readErr, io.EOF) {
			return data, issue("unavailable", "source returned an incomplete read")
		}
		pieces = append(pieces, tailPiece{offset: start, data: append([]byte(nil), buf...)})
		if position == metadata.Size {
			endsWithNewline = buf[len(buf)-1] == '\n'
			if endsWithNewline {
				target++
			}
		}
		for i := len(buf) - 1; i >= 0; i-- {
			if buf[i] == '\n' {
				newlineCount++
				if newlineCount == target {
					cutOffset = start + int64(i) + 1
					foundCut = true
					break
				}
			}
		}
		position = start
		if foundCut {
			break
		}
		if readErr != nil && errors.Is(readErr, io.EOF) {
			break
		}
	}
	if !foundCut && position != 0 {
		return data, issue("budget_exhausted", "tail scan budget exhausted before the requested records were found")
	}
	if !foundCut {
		cutOffset = 0
	}
	base := pieces[len(pieces)-1].offset
	joined := make([]byte, 0, data.scanned)
	for i := len(pieces) - 1; i >= 0; i-- {
		joined = append(joined, pieces[i].data...)
	}
	start := cutOffset - base
	if start < 0 || start > int64(len(joined)) {
		return data, issue("unavailable", "tail scan produced an invalid range")
	}
	content := joined[start:]
	if len(content) > outputQuota {
		return data, issue("budget_exhausted", "tail output exceeds the per-item byte budget")
	}
	end, problem := textPrefix(content, true)
	if problem != nil {
		return data, problem
	}
	data.content = content[:end]
	data.offset, data.endOffset = cutOffset, metadata.Size
	data.eof, data.complete = true, true
	return data, nil
}

type tailPiece struct {
	offset int64
	data   []byte
}

func readAt(ctx context.Context, h Handle, p []byte, offset int64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return h.ReadAt(p, offset)
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
