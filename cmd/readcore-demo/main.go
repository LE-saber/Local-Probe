// readcore-demo reads ONLY an explicitly selected local file. It is not a
// production Source, filesystem sandbox, MCP service or remote-access program.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/LE-saber/Local-Probe/internal/readcore"
)

type selectedFile struct{ file *os.File }
type borrowedHandle struct{ file *os.File }

func (s selectedFile) Open(ctx context.Context, scope readcore.Scope, ref readcore.FileRef) (readcore.Handle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if scope.ConnectionID() != "local-demo" || ref.RootID != "selected" || ref.Path != "input.txt" {
		return nil, os.ErrPermission
	}
	return borrowedHandle{s.file}, nil
}
func (h borrowedHandle) ReadAt(p []byte, off int64) (int, error) { return h.file.ReadAt(p, off) }
func (h borrowedHandle) Close() error                            { return nil } // run owns the pre-opened file.
func (h borrowedHandle) Metadata(ctx context.Context) (readcore.Metadata, error) {
	if err := ctx.Err(); err != nil {
		return readcore.Metadata{}, err
	}
	info, err := h.file.Stat()
	if err != nil {
		return readcore.Metadata{}, err
	}
	if !info.Mode().IsRegular() {
		return readcore.Metadata{}, errors.New("not a regular file")
	}
	return readcore.Metadata{Size: info.Size(), Version: readcore.Version{
		Token: fmt.Sprintf("m-%d-%d", info.Size(), info.ModTime().UnixNano()), Strength: "metadata",
	}}, nil
}

func run(args []string, out, diagnostics io.Writer) error {
	flags := flag.NewFlagSet("readcore-demo", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	filename := flags.String("file", "", "explicit local UTF-8 regular file (never a remote request)")
	offset := flags.Int64("offset", 0, "zero-based byte offset, at a UTF-8 boundary")
	maxBytes := flags.Int("max-bytes", 4096, "maximum page bytes (1..32768)")
	expected := flags.String("expected-version", "", "optional prior version token")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *filename == "" || flags.NArg() != 0 || *offset < 0 || *maxBytes < 1 || *maxBytes > 32768 {
		return errors.New("provide -file, nonnegative -offset and -max-bytes between 1 and 32768")
	}
	// Local operator explicitly selects this file. No path supplied by a model is
	// resolved here. Precheck avoids obvious devices; this is NOT a race defense.
	info, err := os.Lstat(*filename)
	if err != nil {
		return errors.New("cannot inspect selected file")
	}
	if !info.Mode().IsRegular() {
		return errors.New("selected path must be a regular file, not a symlink or device")
	}
	f, err := os.Open(*filename)
	if err != nil {
		return errors.New("cannot open selected file")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return errors.New("selected file changed while opening")
	}
	scope, err := readcore.NewScope("local-demo", "v1", []string{"selected"})
	if err != nil {
		return err
	}
	engine, err := readcore.New(selectedFile{f}, readcore.DefaultLimits())
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := engine.ReadBatch(ctx, scope, []readcore.Request{{
		File:   readcore.FileRef{RootID: "selected", Path: "input.txt"},
		Offset: *offset, MaxBytes: *maxBytes, ExpectedVersion: *expected,
	}})
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		return err
	}
	if result.Failed != 0 {
		return errors.New("read did not succeed; see typed result")
	}
	return nil
}
func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "readcore-demo:", err)
		os.Exit(1)
	}
}
