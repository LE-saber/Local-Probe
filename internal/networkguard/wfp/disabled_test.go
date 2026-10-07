package wfp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LE-saber/Local-Probe/internal/networkguard"
)

func TestDisabledBackendIsPerLeaseAndIdempotent(t *testing.T) {
	backend := NewDisabledBackend()
	now := time.Now()
	guard, err := networkguard.New(backend, networkguard.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	leaseA, err := guard.Prepare(context.Background(), disabledAdmission(now, 1))
	if err != nil {
		t.Fatal(err)
	}
	leaseB, err := guard.Prepare(context.Background(), disabledAdmission(now, 2))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := backend.PlanSummary(leaseA); !ok {
		t.Fatal("lease A plan missing")
	}
	if _, ok := backend.PlanSummary(leaseB); !ok {
		t.Fatal("lease B plan missing")
	}

	if err := backend.LaunchSuspended(context.Background(), leaseA); !errors.Is(err, ErrDisabled) {
		t.Fatalf("LaunchSuspended error = %v, want %v", err, ErrDisabled)
	}
	coverage, err := backend.Activate(context.Background(), leaseA, networkguard.RunHandle{})
	if !errors.Is(err, ErrDisabled) {
		t.Fatalf("Activate error = %v, want %v", err, ErrDisabled)
	}
	if coverage != (networkguard.Coverage{}) {
		t.Fatalf("Activate coverage = %#v, want zero coverage", coverage)
	}

	if _, err := guard.Revoke(context.Background(), leaseA, networkguard.RunHandle{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := backend.PlanSummary(leaseA); ok {
		t.Fatal("lease A plan survived revoke")
	}
	if _, ok := backend.PlanSummary(leaseB); !ok {
		t.Fatal("lease B plan was affected by lease A revoke")
	}
	if _, err := guard.Revoke(context.Background(), leaseA, networkguard.RunHandle{}); err != nil {
		t.Fatalf("repeated revoke: %v", err)
	}
	if _, err := guard.LaunchSuspended(context.Background(), leaseA); !errors.Is(err, networkguard.ErrOperationReplayed) {
		t.Fatalf("replay error = %v, want %v", err, networkguard.ErrOperationReplayed)
	}
}

func TestDisabledBackendPrepareMapsContextFailures(t *testing.T) {
	backend := NewDisabledBackend()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := backend.Prepare(ctx, networkguard.Lease{}, networkguard.Admission{}); !errors.Is(err, networkguard.ErrCancelled) {
		t.Fatalf("cancelled Prepare error = %v, want %v", err, networkguard.ErrCancelled)
	}

	ctx, cancel = context.WithTimeout(context.Background(), 0)
	defer cancel()
	if err := backend.Prepare(ctx, networkguard.Lease{}, networkguard.Admission{}); !errors.Is(err, networkguard.ErrDeadlineExceeded) {
		t.Fatalf("deadline-exceeded Prepare error = %v, want %v", err, networkguard.ErrDeadlineExceeded)
	}
}

func disabledAdmission(now time.Time, nonce byte) networkguard.Admission {
	var nonceDigest [32]byte
	nonceDigest[0] = nonce
	return networkguard.Admission{
		ConnectionID:    "connection",
		ProfileID:       "profile",
		ProfileRevision: "revision",
		CommandID:       "command",
		VariantID:       "variant",
		IdentityDigest:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		NonceDigest:     nonceDigest,
		Deadline:        now.Add(5 * time.Second),
	}
}
