//go:build windows

package previewui

import (
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
	"unsafe"
)

const (
	previewICOHeaderSize = 6
	previewICOEntrySize  = 16
	previewICOTypeIcon   = 1
	previewICOEntryLimit = 256

	previewIconDefaultSize = 32
	previewIconMaxSize     = 256
	iconResourceVersion    = 0x00030000
)

var (
	//go:embed assets/local-probe-preview.ico
	previewIconICO []byte

	procCreateIconFromResourceEx = user32.NewProc("CreateIconFromResourceEx")
	procDestroyIcon              = user32.NewProc("DestroyIcon")

	ErrInvalidPreviewIcon = errors.New("invalid preview icon resource")
	ErrPreviewIconSize    = errors.New("invalid preview icon size")
	ErrPreviewIconCreate  = errors.New("preview icon creation failed")
)

// previewICOEntry is the bounded, validated part of an ICO directory entry.
// A zero width or height in an ICO means 256 pixels and is normalized while
// parsing so selection code never has to special-case that encoding.
type previewICOEntry struct {
	Width      int
	Height     int
	ColorCnt   uint8
	Planes     uint16
	BitCount   uint16
	ByteSize   uint32
	ByteOffset uint32
}

// parsePreviewICO validates only the ICO header and directory. It does not
// decode the image payload; Windows owns that job. Keeping this parser small
// makes the embedded resource boundary easy to test and prevents malformed
// offsets from becoming unsafe slices later.
func parsePreviewICO(data []byte) ([]previewICOEntry, error) {
	if len(data) < previewICOHeaderSize {
		return nil, fmt.Errorf("%w: header is truncated", ErrInvalidPreviewIcon)
	}
	if binary.LittleEndian.Uint16(data[0:2]) != 0 {
		return nil, fmt.Errorf("%w: reserved field is non-zero", ErrInvalidPreviewIcon)
	}
	if binary.LittleEndian.Uint16(data[2:4]) != previewICOTypeIcon {
		return nil, fmt.Errorf("%w: resource is not an icon", ErrInvalidPreviewIcon)
	}
	count := int(binary.LittleEndian.Uint16(data[4:6]))
	if count == 0 || count > previewICOEntryLimit {
		return nil, fmt.Errorf("%w: invalid image count", ErrInvalidPreviewIcon)
	}
	if count > (len(data)-previewICOHeaderSize)/previewICOEntrySize {
		return nil, fmt.Errorf("%w: directory is truncated", ErrInvalidPreviewIcon)
	}
	directoryEnd := previewICOHeaderSize + count*previewICOEntrySize
	entries := make([]previewICOEntry, 0, count)
	for index := 0; index < count; index++ {
		offset := previewICOHeaderSize + index*previewICOEntrySize
		width := int(data[offset])
		if width == 0 {
			width = previewIconMaxSize
		}
		height := int(data[offset+1])
		if height == 0 {
			height = previewIconMaxSize
		}
		byteSize := binary.LittleEndian.Uint32(data[offset+8 : offset+12])
		byteOffset := binary.LittleEndian.Uint32(data[offset+12 : offset+16])
		if byteSize == 0 || uint64(byteOffset) < uint64(directoryEnd) || uint64(byteOffset)+uint64(byteSize) > uint64(len(data)) {
			return nil, fmt.Errorf("%w: image %d is outside the resource", ErrInvalidPreviewIcon, index)
		}
		entries = append(entries, previewICOEntry{
			Width:      width,
			Height:     height,
			ColorCnt:   data[offset+2],
			Planes:     binary.LittleEndian.Uint16(data[offset+4 : offset+6]),
			BitCount:   binary.LittleEndian.Uint16(data[offset+6 : offset+8]),
			ByteSize:   byteSize,
			ByteOffset: byteOffset,
		})
	}
	return entries, nil
}

// selectPreviewICOEntry picks the closest resource to the requested square
// size. Ties prefer an entry that is at least as large as requested, avoiding
// an avoidable upscale when the ICO contains multiple nearby sizes.
func selectPreviewICOEntry(entries []previewICOEntry, desired int) (previewICOEntry, error) {
	if desired == 0 {
		desired = previewIconDefaultSize
	}
	if desired < 1 || desired > previewIconMaxSize {
		return previewICOEntry{}, ErrPreviewIconSize
	}
	if len(entries) == 0 {
		return previewICOEntry{}, fmt.Errorf("%w: no directory entries", ErrInvalidPreviewIcon)
	}
	best := entries[0]
	bestScore := iconEntryDistance(best, desired)
	for _, candidate := range entries[1:] {
		score := iconEntryDistance(candidate, desired)
		if score < bestScore || (score == bestScore && preferIconEntry(candidate, best, desired)) {
			best = candidate
			bestScore = score
		}
	}
	return best, nil
}

func iconEntryDistance(entry previewICOEntry, desired int) int {
	return absInt(entry.Width-desired) + absInt(entry.Height-desired)
}

func preferIconEntry(candidate, current previewICOEntry, desired int) bool {
	candidateAdequate := candidate.Width >= desired && candidate.Height >= desired
	currentAdequate := current.Width >= desired && current.Height >= desired
	if candidateAdequate != currentAdequate {
		return candidateAdequate
	}
	return candidate.Width*candidate.Height > current.Width*current.Height
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

// loadPreviewIcon creates an owned HICON from the embedded ICO. The returned
// handle must be passed to destroyPreviewIcon after the window class and tray
// icon no longer reference it. A requested size of zero selects the default
// 32px resource; sizes outside the ICO's 1..256px range are rejected.
func loadPreviewIcon(size int) (uintptr, error) {
	entries, err := parsePreviewICO(previewIconICO)
	if err != nil {
		return 0, err
	}
	entry, err := selectPreviewICOEntry(entries, size)
	if err != nil {
		return 0, err
	}
	start := int(entry.ByteOffset)
	end := start + int(entry.ByteSize)
	resource := previewIconICO[start:end]
	icon, _, _ := procCreateIconFromResourceEx.Call(
		uintptr(unsafe.Pointer(&resource[0])),
		uintptr(len(resource)),
		1,
		iconResourceVersion,
		uintptr(entry.Width),
		uintptr(entry.Height),
		0,
	)
	if icon == 0 {
		return 0, ErrPreviewIconCreate
	}
	return icon, nil
}

// destroyPreviewIcon releases an HICON returned by loadPreviewIcon. It is a
// best-effort cleanup helper because there is no useful recovery if Windows
// rejects destruction during process teardown.
func destroyPreviewIcon(icon uintptr) {
	if icon == 0 {
		return
	}
	_, _, _ = procDestroyIcon.Call(icon)
}
