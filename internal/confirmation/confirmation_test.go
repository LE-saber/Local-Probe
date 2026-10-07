package confirmation

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"
)

func testRequest() Request {
	return Request{
		ConnectionID: "connection-a", ProfileID: "read-project", ProfileRevision: "r7",
		CommandID: "codex_version", VariantID: "short", RequestNonce: "nonce-123",
		ResolvedInputDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
}

func testManager(t *testing.T, now *time.Time) *Manager {
	t.Helper()
	secret := make([]byte, sha256.Size)
	for i := range secret {
		secret[i] = byte(i + 1)
	}
	manager, err := NewWithClock(secret, func() time.Time { return *now })
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func TestMintConsumeBindsRequestAndIsOneTime(t *testing.T) {
	now := time.Unix(1700000000, 0)
	manager := testManager(t, &now)
	request := testRequest()
	capability, err := manager.Mint(request, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if capability.String() == "" {
		t.Fatal("Mint returned empty capability")
	}
	parsed, err := manager.Parse(capability.String())
	if err != nil {
		t.Fatal(err)
	}
	wrong := request
	wrong.VariantID = "long"
	if err := manager.Consume(parsed, wrong); !errors.Is(err, ErrRequestMismatch) {
		t.Fatalf("wrong variant result = %v", err)
	}
	if err := manager.Consume(parsed, request); err != nil {
		t.Fatalf("Consume failed: %v", err)
	}
	if err := manager.Consume(parsed, request); !errors.Is(err, ErrConsumed) {
		t.Fatalf("replay result = %v", err)
	}
	if _, err := json.Marshal(capability); err == nil {
		t.Fatal("capability was JSON serializable")
	}
}

func TestResolvedInputDigestIsRequiredAndBound(t *testing.T) {
	now := time.Unix(1700000000, 0)
	manager := testManager(t, &now)
	missing := testRequest()
	missing.ResolvedInputDigest = ""
	if _, err := manager.Mint(missing, time.Minute); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing resolved input digest accepted: %v", err)
	}
	invalid := testRequest()
	invalid.ResolvedInputDigest = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if _, err := manager.Mint(invalid, time.Minute); !errors.Is(err, ErrInvalid) {
		t.Fatalf("uppercase resolved input digest accepted: %v", err)
	}
	capability, err := manager.Mint(testRequest(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	wrong := testRequest()
	w := []byte(wrong.ResolvedInputDigest)
	w[0] = 'b'
	wrong.ResolvedInputDigest = string(w)
	if err := manager.Consume(capability, wrong); !errors.Is(err, ErrRequestMismatch) {
		t.Fatalf("resolved input digest mismatch result = %v", err)
	}
	if err := manager.Consume(capability, testRequest()); err != nil {
		t.Fatalf("matching resolved input digest rejected: %v", err)
	}
}

func TestLegacyTokenVersionIsRejected(t *testing.T) {
	now := time.Unix(1700000000, 0)
	manager := testManager(t, &now)
	capability, err := manager.Mint(testRequest(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(capability.String())
	if err != nil {
		t.Fatal(err)
	}
	raw[0] = 1
	// Parse only checks the bounded token shape; Consume must reject this
	// legacy version byte in decode before any request can be authorized.
	legacy, err := manager.Parse(base64.RawURLEncoding.EncodeToString(raw))
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Consume(legacy, testRequest()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("legacy token version result = %v", err)
	}
}

func TestTamperAndExpiryFailClosed(t *testing.T) {
	now := time.Unix(1700000000, 0)
	manager := testManager(t, &now)
	capability, err := manager.Mint(testRequest(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(capability.String())
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 1
	tampered, err := manager.Parse(base64.RawURLEncoding.EncodeToString(raw))
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Consume(tampered, testRequest()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("tampered capability result = %v", err)
	}
	now = now.Add(time.Minute)
	if err := manager.Consume(capability, testRequest()); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired capability result = %v", err)
	}
}

func TestConsumeConcurrentExactlyOnce(t *testing.T) {
	now := time.Unix(1700000000, 0)
	manager := testManager(t, &now)
	capability, err := manager.Mint(testRequest(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := manager.Parse(capability.String())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- manager.Consume(parsed, testRequest())
		}()
	}
	wg.Wait()
	close(results)
	var success int
	for result := range results {
		if result == nil {
			success++
		} else if !errors.Is(result, ErrConsumed) {
			t.Fatalf("unexpected concurrent consume result: %v", result)
		}
	}
	if success != 1 {
		t.Fatalf("successful concurrent consumes = %d, want 1", success)
	}
}

func TestSecretAndLifetimeBounds(t *testing.T) {
	if _, err := New(make([]byte, sha256.Size-1)); !errors.Is(err, ErrWeakSecret) {
		t.Fatalf("short secret result = %v", err)
	}
	now := time.Unix(1700000000, 0)
	manager := testManager(t, &now)
	if _, err := manager.Mint(testRequest(), 0); err == nil {
		t.Fatal("zero lifetime accepted")
	}
	if _, err := manager.Mint(testRequest(), maxTTL+time.Nanosecond); err == nil {
		t.Fatal("long lifetime accepted")
	}
}

func TestConsumedCapacityFailsClosedWithoutEvictingLiveRecords(t *testing.T) {
	now := time.Unix(1700000000, 0)
	manager := testManager(t, &now)
	consumed := make([]Capability, 0, 4096)
	requests := make([]Request, 0, 4096)
	for i := 0; i < 4096; i++ {
		request := testRequest()
		request.RequestNonce = "nonce-capacity-" + strconv.Itoa(i)
		capability, err := manager.Mint(request, time.Minute)
		if err != nil {
			t.Fatalf("Mint(%d): %v", i, err)
		}
		if err := manager.Consume(capability, request); err != nil {
			t.Fatalf("Consume(%d): %v", i, err)
		}
		consumed = append(consumed, capability)
		requests = append(requests, request)
	}
	pendingRequest := testRequest()
	pendingRequest.RequestNonce = "nonce-pending"
	pending, err := manager.Mint(pendingRequest, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Consume(pending, pendingRequest); !errors.Is(err, ErrCapacity) {
		t.Fatalf("capacity result = %v", err)
	}
	for i := range consumed {
		if err := manager.Consume(consumed[i], requests[i]); !errors.Is(err, ErrConsumed) {
			t.Fatalf("live consumed token %d was evicted: %v", i, err)
		}
	}
	now = now.Add(2 * time.Minute)
	if err := manager.Consume(pending, pendingRequest); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired pending token result = %v", err)
	}
	newRequest := testRequest()
	newRequest.RequestNonce = "nonce-after-expiry"
	fresh, err := manager.Mint(newRequest, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Consume(fresh, newRequest); err != nil {
		t.Fatalf("expired replay entries did not free capacity: %v", err)
	}
}
