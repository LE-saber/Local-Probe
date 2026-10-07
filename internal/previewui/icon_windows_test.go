//go:build windows

package previewui

import (
	"encoding/binary"
	"errors"
	"testing"
)

func TestParsePreviewICOEmbedded(t *testing.T) {
	entries, err := parsePreviewICO(previewIconICO)
	if err != nil {
		t.Fatalf("parse embedded icon: %v", err)
	}
	if len(entries) != 9 {
		t.Fatalf("entry count = %d, want 9", len(entries))
	}
	want := []int{16, 20, 24, 32, 40, 48, 64, 128, 256}
	for index, entry := range entries {
		if entry.Width != want[index] || entry.Height != want[index] {
			t.Errorf("entry %d size = %dx%d, want %dx%d", index, entry.Width, entry.Height, want[index], want[index])
		}
		if entry.ByteSize == 0 {
			t.Errorf("entry %d has an empty payload", index)
		}
	}
}

func TestParsePreviewICORejectsMalformedDirectory(t *testing.T) {
	valid := func() []byte {
		data := make([]byte, previewICOHeaderSize+previewICOEntrySize+1)
		binary.LittleEndian.PutUint16(data[2:4], previewICOTypeIcon)
		binary.LittleEndian.PutUint16(data[4:6], 1)
		data[6] = 16
		data[7] = 16
		binary.LittleEndian.PutUint32(data[14:18], 1)
		binary.LittleEndian.PutUint32(data[18:22], uint32(previewICOHeaderSize+previewICOEntrySize))
		data[len(data)-1] = 0x89
		return data
	}

	tests := []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{name: "short header", mutate: func(data []byte) []byte { return data[:5] }},
		{name: "reserved", mutate: func(data []byte) []byte { data[0] = 1; return data }},
		{name: "wrong type", mutate: func(data []byte) []byte {
			binary.LittleEndian.PutUint16(data[2:4], 2)
			return data
		}},
		{name: "zero count", mutate: func(data []byte) []byte {
			binary.LittleEndian.PutUint16(data[4:6], 0)
			return data
		}},
		{name: "truncated directory", mutate: func(data []byte) []byte {
			binary.LittleEndian.PutUint16(data[4:6], 2)
			return data
		}},
		{name: "empty payload", mutate: func(data []byte) []byte {
			binary.LittleEndian.PutUint32(data[14:18], 0)
			return data
		}},
		{name: "offset overlaps directory", mutate: func(data []byte) []byte {
			binary.LittleEndian.PutUint32(data[18:22], previewICOHeaderSize)
			return data
		}},
		{name: "payload out of bounds", mutate: func(data []byte) []byte {
			binary.LittleEndian.PutUint32(data[14:18], 2)
			return data
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := test.mutate(valid())
			if _, err := parsePreviewICO(data); !errors.Is(err, ErrInvalidPreviewIcon) {
				t.Fatalf("parse error = %v, want ErrInvalidPreviewIcon", err)
			}
		})
	}
}

func TestSelectPreviewICOEntry(t *testing.T) {
	entries := []previewICOEntry{{Width: 16, Height: 16}, {Width: 20, Height: 20}, {Width: 24, Height: 24}, {Width: 32, Height: 32}, {Width: 64, Height: 64}}
	tests := []struct {
		desired int
		want    int
	}{
		{desired: 0, want: 32},
		{desired: 18, want: 20},
		{desired: 24, want: 24},
		{desired: 60, want: 64},
		{desired: 100, want: 64},
	}
	for _, test := range tests {
		got, err := selectPreviewICOEntry(entries, test.desired)
		if err != nil {
			t.Fatalf("select %d: %v", test.desired, err)
		}
		if got.Width != test.want {
			t.Errorf("select %d = %d, want %d", test.desired, got.Width, test.want)
		}
	}
	for _, desired := range []int{-1, 257} {
		if _, err := selectPreviewICOEntry(entries, desired); !errors.Is(err, ErrPreviewIconSize) {
			t.Errorf("select %d error = %v, want ErrPreviewIconSize", desired, err)
		}
	}
	if _, err := selectPreviewICOEntry(nil, 32); !errors.Is(err, ErrInvalidPreviewIcon) {
		t.Errorf("empty select error = %v, want ErrInvalidPreviewIcon", err)
	}
}

func TestLoadPreviewIconAndDestroy(t *testing.T) {
	for _, size := range []int{0, 16, 32, 128, 256} {
		icon, err := loadPreviewIcon(size)
		if err != nil {
			t.Fatalf("load icon size %d: %v", size, err)
		}
		if icon == 0 {
			t.Fatalf("load icon size %d returned a null handle", size)
		}
		destroyPreviewIcon(icon)
	}
}

func TestLoadPreviewIconRejectsInvalidSize(t *testing.T) {
	for _, size := range []int{-1, 257} {
		if _, err := loadPreviewIcon(size); !errors.Is(err, ErrPreviewIconSize) {
			t.Errorf("load icon size %d error = %v, want ErrPreviewIconSize", size, err)
		}
	}
}
