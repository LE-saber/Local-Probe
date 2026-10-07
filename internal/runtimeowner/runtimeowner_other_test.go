//go:build !windows

package runtimeowner

import (
	"errors"
	"testing"
)

func TestOtherPlatformsAreExplicitlyUnsupported(t *testing.T) {
	if ProductionReady() {
		t.Fatal("production readiness must remain closed")
	}
	job, err := NewJob()
	if job != nil {
		t.Fatal("unsupported NewJob returned a job")
	}
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("NewJob error = %v, want %v", err, ErrUnsupported)
	}
}
