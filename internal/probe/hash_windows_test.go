//go:build windows

package probe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"testing"
)

func TestWindowsVersionPolicyHashesTheAuditedImageBeforeLaunch(t *testing.T) {
	path := copyExecutable(t, helperPath("fake-node"), executableName("fake-node"))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	descriptor, err := AuditExecutable(ToolVersionGeneric, path)
	if err != nil {
		t.Fatal(err)
	}
	policy := DefaultVersionExecutionPolicy()
	policy.SHA256 = hex.EncodeToString(digest[:])
	result, err := ToolVersionWithPolicy(context.Background(), descriptor, []string{"-v"}, policy)
	if err != nil {
		t.Fatalf("matching digest rejected: %v", err)
	}
	if result.Version != "1.2.3" {
		t.Fatalf("unexpected generic version: %#v", result)
	}
}

func TestWindowsVersionPolicyRejectsHashMismatchBeforeLaunch(t *testing.T) {
	path := copyExecutable(t, helperPath("fake-node"), executableName("fake-node"))
	descriptor, err := AuditExecutable(ToolVersionGeneric, path)
	if err != nil {
		t.Fatal(err)
	}
	policy := DefaultVersionExecutionPolicy()
	policy.SHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
	if _, err := ToolVersionWithPolicy(context.Background(), descriptor, []string{"--version"}, policy); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("want ErrHashMismatch, got %v", err)
	}
}

func TestWindowsVersionPolicyRejectsUnsafeVariant(t *testing.T) {
	path := copyExecutable(t, helperPath("fake-node"), executableName("fake-node"))
	descriptor, err := AuditExecutable(ToolVersionGeneric, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ToolVersionWithPolicy(context.Background(), descriptor, []string{"-c", "side-effect"}, DefaultVersionExecutionPolicy()); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput, got %v", err)
	}
}
