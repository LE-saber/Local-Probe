package operations

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
)

func LoadCatalog(path string) (Catalog, error) {
	var c Catalog
	if !filepath.IsAbs(path) {
		return c, ErrInvalid
	}
	f, err := os.Open(path)
	if err != nil {
		return c, ErrStorage
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil || len(b) > 64<<10 {
		return c, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF {
		return c, ErrInvalid
	}
	return c, nil
}
