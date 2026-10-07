package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/LE-saber/Local-Probe/internal/readcore"
)

func TestDemoReadsRealFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(path, []byte("alpha beta gamma"), 0600); err != nil {
		t.Fatal(err)
	}
	var out, diagnostics bytes.Buffer
	if err := run([]string{"-file", path, "-offset", "6", "-max-bytes", "4"}, &out, &diagnostics); err != nil {
		t.Fatal(err)
	}
	var result readcore.BatchResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Failed != 0 || result.Items[0].Content != "beta" || result.Items[0].Version.Strength != "metadata" {
		t.Fatalf("bad result %+v", result)
	}
}

func TestDemoRejectsInvalidArgumentsAndDirectories(t *testing.T) {
	for _, args := range [][]string{nil, {"-file", "x", "-offset", "-1"}, {"-file", "x", "-max-bytes", "0"}, {"-file", t.TempDir()}, {"-file", "missing-file"}, {"-unknown"}} {
		var out, diagnostics bytes.Buffer
		if err := run(args, &out, &diagnostics); err == nil {
			t.Fatalf("accepted invalid args %v", args)
		}
	}
}

func TestDemoReportsStaleVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(path, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	var out, diagnostics bytes.Buffer
	err := run([]string{"-file", path, "-expected-version", "old"}, &out, &diagnostics)
	if err == nil {
		t.Fatal("expected failed read")
	}
	var result readcore.BatchResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Items[0].Error.Code != "stale_version" || result.Items[0].Content != "" {
		t.Fatal("stale content returned")
	}
}
