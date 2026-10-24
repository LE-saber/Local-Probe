//go:build windows

package probe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTrustedHostRealExecution(t *testing.T) {
	for _, action := range []string{"fake-node", "fake-timeout", "fake-overoutput"} {
		t.Run(action, func(t *testing.T) {
			exe := helperPath(action)
			descriptor, err := AuditExecutable(ToolVersionGeneric, exe)
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(exe)
			if err != nil {
				t.Fatal(err)
			}
			hash := sha256.Sum256(data)
			dir := t.TempDir()
			spec := TrustedProcessSpec{Executable: descriptor, SHA256: hex.EncodeToString(hash[:]), Args: []string{"--version"}, Directory: dir, Environment: []string{"SystemRoot=" + filepath.Clean(os.Getenv("SystemRoot")), "TEMP=" + dir, "TMP=" + dir}, Timeout: 3 * time.Second, OutputBytes: 4096}
			if action == "fake-timeout" {
				spec.Timeout = 250 * time.Millisecond
			}
			result, err := RunTrustedProcess(context.Background(), spec)
			if action == "fake-node" {
				if err != nil || string(result.Stdout) != "v1.2.3\n" || result.ExitCode != 0 {
					t.Fatalf("result=%+v err=%v", result, err)
				}
			} else {
				want := ErrOutputLimit
				if action == "fake-timeout" {
					want = ErrDeadlineExceeded
				}
				if !errors.Is(err, want) {
					t.Fatalf("%s error=%v, want %v", action, err, want)
				}
			}
		})
	}
}
