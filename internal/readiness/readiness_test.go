package readiness

import (
	"bytes"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

const (
	testConnection = "connection-a"
	testRevision   = "revision-1"
)

type testClock struct {
	mu  sync.RWMutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.now
}

func (c *testClock) Set(now time.Time) {
	c.mu.Lock()
	c.now = now
	c.mu.Unlock()
}

func entropyBytes(seed byte, size int) []byte {
	data := make([]byte, size)
	for i := range data {
		data[i] = seed + byte(i) + 1
		if data[i] == 0 {
			data[i] = 1
		}
	}
	return data
}

func testIssuer(t *testing.T, seed byte) *Issuer {
	t.Helper()
	issuer, err := NewIssuerWithEntropy(bytes.NewReader(entropyBytes(seed, 4096)))
	if err != nil {
		t.Fatalf("NewIssuerWithEntropy: %v", err)
	}
	return issuer
}

func testSession(t *testing.T, seed byte, connectionID, revision string, generation uint64) *Session {
	t.Helper()
	return testSessionFromIssuer(t, testIssuer(t, seed), connectionID, revision, generation)
}

func testSessionFromIssuer(t *testing.T, issuer *Issuer, connectionID, revision string, generation uint64) *Session {
	t.Helper()
	session, err := issuer.NewSession(connectionID, revision, generation)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	return session
}

func testContext(t *testing.T, session *Session) Context {
	t.Helper()
	context, err := session.NewContext()
	if err != nil {
		t.Fatalf("NewContext: %v", err)
	}
	return context
}

func testAttestations(t *testing.T, session *Session, context Context, observedAt time.Time) (ChildAttestation, LocalMCPAttestation, RemoteTunnelAttestation) {
	t.Helper()
	child, err := session.NewChildAttestation(context, observedAt, 42, 7, true)
	if err != nil {
		t.Fatalf("NewChildAttestation: %v", err)
	}
	local, err := session.NewLocalMCPAttestation(context, observedAt, AuthAuthenticated, CheckReady, CheckReady)
	if err != nil {
		t.Fatalf("NewLocalMCPAttestation: %v", err)
	}
	remote, err := session.NewRemoteTunnelAttestation(context, observedAt, AuthAuthenticated, TunnelHealthReady, TunnelHAReady)
	if err != nil {
		t.Fatalf("NewRemoteTunnelAttestation: %v", err)
	}
	return child, local, remote
}

func testEvaluator(t *testing.T, clock Clock, freshness time.Duration, nonceLimit int) *Evaluator {
	t.Helper()
	evaluator, err := New(Options{Clock: clock, Freshness: freshness, NonceLimit: nonceLimit})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return evaluator
}

func TestEvaluateRequiresAllFreshPositiveAttestations(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	clock := &testClock{now: now}
	evaluator := testEvaluator(t, clock, time.Minute, 8)
	session := testSession(t, 1, testConnection, testRevision, 1)
	context := testContext(t, session)
	child, local, remote := testAttestations(t, session, context, now)

	decision := evaluator.Evaluate(context, child, local, remote)
	if !decision.Ready() || decision.Reason() != ReasonReady || decision.AuthFailure() != AuthFailureNone {
		t.Fatalf("decision = %#v", decision)
	}

	duplicate := evaluator.Evaluate(context, child, local, remote)
	if duplicate.Ready() || duplicate.Reason() != ReasonDuplicateNonce {
		t.Fatalf("duplicate = %#v", duplicate)
	}
}

func TestEvaluateRejectsMissingEvidenceAndStandaloneHealth(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	session := testSession(t, 2, testConnection, testRevision, 1)
	context := testContext(t, session)
	child, local, remote := testAttestations(t, session, context, now)

	cases := []struct {
		name   string
		mutate func() (ChildAttestation, LocalMCPAttestation, RemoteTunnelAttestation)
		want   Reason
	}{
		{name: "missing child", mutate: func() (ChildAttestation, LocalMCPAttestation, RemoteTunnelAttestation) {
			return ChildAttestation{}, local, remote
		}, want: ReasonChildMissing},
		{name: "missing local mcp", mutate: func() (ChildAttestation, LocalMCPAttestation, RemoteTunnelAttestation) {
			return child, LocalMCPAttestation{}, remote
		}, want: ReasonLocalMCPMissing},
		{name: "missing remote tunnel", mutate: func() (ChildAttestation, LocalMCPAttestation, RemoteTunnelAttestation) {
			return child, local, RemoteTunnelAttestation{}
		}, want: ReasonRemoteTunnelMissing},
		{name: "process identity without ownership", mutate: func() (ChildAttestation, LocalMCPAttestation, RemoteTunnelAttestation) {
			unowned, err := session.NewChildAttestation(context, now, 42, 7, false)
			if err != nil {
				t.Fatalf("NewChildAttestation: %v", err)
			}
			return unowned, local, remote
		}, want: ReasonChildNotOwned},
		{name: "tunnel health without HA", mutate: func() (ChildAttestation, LocalMCPAttestation, RemoteTunnelAttestation) {
			notHA, err := session.NewRemoteTunnelAttestation(context, now, AuthAuthenticated, TunnelHealthReady, TunnelHANotReady)
			if err != nil {
				t.Fatalf("NewRemoteTunnelAttestation: %v", err)
			}
			return child, local, notHA
		}, want: ReasonRemoteHANotReady},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			evaluator := testEvaluator(t, ClockFunc(func() time.Time { return now }), time.Minute, 8)
			child, local, remote := test.mutate()
			got := evaluator.Evaluate(context, child, local, remote)
			if got.Ready() || got.Reason() != test.want {
				t.Fatalf("decision = %#v, want reason %q", got, test.want)
			}
		})
	}
}

func TestEvaluateRejectsStaleAndFutureAttestations(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	clock := &testClock{now: now}
	evaluator := testEvaluator(t, clock, 10*time.Second, 8)

	staleSession := testSession(t, 3, testConnection, testRevision, 1)
	staleContext := testContext(t, staleSession)
	_, staleLocal, staleRemote := testAttestations(t, staleSession, staleContext, now)
	oldChild, err := staleSession.NewChildAttestation(staleContext, now.Add(-11*time.Second), 42, 7, true)
	if err != nil {
		t.Fatalf("NewChildAttestation: %v", err)
	}
	if decision := evaluator.Evaluate(staleContext, oldChild, staleLocal, staleRemote); decision.Reason() != ReasonChildStale {
		t.Fatalf("stale child = %#v", decision)
	}
	freshChild, err := staleSession.NewChildAttestation(staleContext, now, 42, 7, true)
	if err != nil {
		t.Fatalf("NewChildAttestation: %v", err)
	}
	if decision := evaluator.Evaluate(staleContext, freshChild, staleLocal, staleRemote); decision.Reason() != ReasonDuplicateNonce {
		t.Fatalf("stale nonce replay = %#v", decision)
	}

	localSession := testSession(t, 4, testConnection, testRevision, 1)
	localContext := testContext(t, localSession)
	localChild, _, localRemote := testAttestations(t, localSession, localContext, now)
	futureLocal, err := localSession.NewLocalMCPAttestation(localContext, now.Add(time.Second), AuthAuthenticated, CheckReady, CheckReady)
	if err != nil {
		t.Fatalf("NewLocalMCPAttestation: %v", err)
	}
	if decision := evaluator.Evaluate(localContext, localChild, futureLocal, localRemote); decision.Reason() != ReasonLocalMCPStale {
		t.Fatalf("future local = %#v", decision)
	}

	remoteSession := testSession(t, 5, testConnection, testRevision, 1)
	remoteContext := testContext(t, remoteSession)
	remoteChild, remoteLocal, _ := testAttestations(t, remoteSession, remoteContext, now)
	futureRemote, err := remoteSession.NewRemoteTunnelAttestation(remoteContext, now.Add(time.Second), AuthAuthenticated, TunnelHealthReady, TunnelHAReady)
	if err != nil {
		t.Fatalf("NewRemoteTunnelAttestation: %v", err)
	}
	if decision := evaluator.Evaluate(remoteContext, remoteChild, remoteLocal, futureRemote); decision.Reason() != ReasonRemoteTunnelStale {
		t.Fatalf("future remote = %#v", decision)
	}
}

func TestEvaluateRejectsContextAndIssuerMismatches(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	baseSession := testSession(t, 6, testConnection, testRevision, 1)
	baseContext := testContext(t, baseSession)
	child, local, remote := testAttestations(t, baseSession, baseContext, now)

	cases := []struct {
		name string
		want Reason
		make func(t *testing.T) (Context, ChildAttestation, LocalMCPAttestation, RemoteTunnelAttestation)
	}{
		{name: "connection", want: ReasonConnectionMismatch, make: func(t *testing.T) (Context, ChildAttestation, LocalMCPAttestation, RemoteTunnelAttestation) {
			wrongSession := testSession(t, 7, "connection-b", testRevision, 1)
			wrongContext := testContext(t, wrongSession)
			wrongChild, err := wrongSession.NewChildAttestation(wrongContext, now, 42, 7, true)
			if err != nil {
				t.Fatalf("NewChildAttestation: %v", err)
			}
			return baseContext, wrongChild, local, remote
		}},
		{name: "revision", want: ReasonRevisionMismatch, make: func(t *testing.T) (Context, ChildAttestation, LocalMCPAttestation, RemoteTunnelAttestation) {
			wrongSession := testSession(t, 8, testConnection, "revision-2", 1)
			wrongContext := testContext(t, wrongSession)
			wrongLocal, err := wrongSession.NewLocalMCPAttestation(wrongContext, now, AuthAuthenticated, CheckReady, CheckReady)
			if err != nil {
				t.Fatalf("NewLocalMCPAttestation: %v", err)
			}
			return baseContext, child, wrongLocal, remote
		}},
		{name: "generation", want: ReasonGenerationMismatch, make: func(t *testing.T) (Context, ChildAttestation, LocalMCPAttestation, RemoteTunnelAttestation) {
			wrongSession := testSession(t, 9, testConnection, testRevision, 2)
			wrongContext := testContext(t, wrongSession)
			wrongRemote, err := wrongSession.NewRemoteTunnelAttestation(wrongContext, now, AuthAuthenticated, TunnelHealthReady, TunnelHAReady)
			if err != nil {
				t.Fatalf("NewRemoteTunnelAttestation: %v", err)
			}
			return baseContext, child, local, wrongRemote
		}},
		{name: "nonce", want: ReasonNonceMismatch, make: func(t *testing.T) (Context, ChildAttestation, LocalMCPAttestation, RemoteTunnelAttestation) {
			wrongContext := testContext(t, baseSession)
			wrongRemote, err := baseSession.NewRemoteTunnelAttestation(wrongContext, now, AuthAuthenticated, TunnelHealthReady, TunnelHAReady)
			if err != nil {
				t.Fatalf("NewRemoteTunnelAttestation: %v", err)
			}
			return baseContext, child, local, wrongRemote
		}},
		{name: "issuer capability", want: ReasonIssuerMismatch, make: func(t *testing.T) (Context, ChildAttestation, LocalMCPAttestation, RemoteTunnelAttestation) {
			wrongSession := testSession(t, 10, testConnection, testRevision, 1)
			wrongContext := testContext(t, wrongSession)
			wrongChild, err := wrongSession.NewChildAttestation(wrongContext, now, 42, 7, true)
			if err != nil {
				t.Fatalf("NewChildAttestation: %v", err)
			}
			return baseContext, wrongChild, local, remote
		}},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			current, child, local, remote := test.make(t)
			decision := testEvaluator(t, ClockFunc(func() time.Time { return now }), time.Minute, 8).Evaluate(current, child, local, remote)
			if decision.Ready() || decision.Reason() != test.want {
				t.Fatalf("decision = %#v, want reason %q", decision, test.want)
			}
		})
	}
}

func TestEvaluateRejectsForgedContextWithoutIssuerCapability(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	session := testSession(t, 11, testConnection, testRevision, 1)
	context := testContext(t, session)
	child, local, remote := testAttestations(t, session, context, now)
	otherSession := testSession(t, 12, testConnection, testRevision, 1)
	if _, err := otherSession.NewLocalMCPAttestation(context, now, AuthAuthenticated, CheckReady, CheckReady); !errors.Is(err, ErrInvalidAttestation) {
		t.Fatalf("cross-session constructor error = %v", err)
	}
	forged := Context{
		connectionID: context.connectionID,
		revision:     context.revision,
		generation:   context.generation,
		nonce:        context.nonce,
	}
	if forged.valid() {
		t.Fatal("forged context unexpectedly valid")
	}
	decision := testEvaluator(t, ClockFunc(func() time.Time { return now }), time.Minute, 8).Evaluate(forged, child, local, remote)
	if decision.Ready() || decision.Reason() != ReasonInvalidContext {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestEvaluateClassifiesAuthenticationFailuresAndConsumesNonce(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	for _, test := range []struct {
		name string
		auth Authentication
		want AuthFailureClass
	}{
		{name: "missing", auth: AuthMissing, want: AuthFailureMissing},
		{name: "rejected", auth: AuthRejected, want: AuthFailureRejected},
		{name: "expired", auth: AuthExpired, want: AuthFailureExpired},
		{name: "unknown", auth: AuthUnknown, want: AuthFailureUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := testSession(t, byte(20+len(test.name)), testConnection, testRevision, 1)
			context := testContext(t, session)
			child, _, remote := testAttestations(t, session, context, now)
			failedLocal, err := session.NewLocalMCPAttestation(context, now, test.auth, CheckReady, CheckReady)
			if err != nil {
				t.Fatalf("NewLocalMCPAttestation: %v", err)
			}
			evaluator := testEvaluator(t, ClockFunc(func() time.Time { return now }), time.Minute, 8)
			decision := evaluator.Evaluate(context, child, failedLocal, remote)
			if decision.Ready() || decision.Reason() != ReasonLocalAuthFailed || decision.AuthFailure() != test.want {
				t.Fatalf("decision = %#v, want auth failure %q", decision, test.want)
			}
			freshLocal, err := session.NewLocalMCPAttestation(context, now, AuthAuthenticated, CheckReady, CheckReady)
			if err != nil {
				t.Fatalf("NewLocalMCPAttestation: %v", err)
			}
			if replay := evaluator.Evaluate(context, child, freshLocal, remote); replay.Reason() != ReasonDuplicateNonce {
				t.Fatalf("replay = %#v", replay)
			}
		})
	}

	session := testSession(t, 30, testConnection, testRevision, 1)
	context := testContext(t, session)
	child, local, _ := testAttestations(t, session, context, now)
	failedRemote, err := session.NewRemoteTunnelAttestation(context, now, AuthRejected, TunnelHealthReady, TunnelHAReady)
	if err != nil {
		t.Fatalf("NewRemoteTunnelAttestation: %v", err)
	}
	decision := testEvaluator(t, ClockFunc(func() time.Time { return now }), time.Minute, 8).Evaluate(context, child, local, failedRemote)
	if decision.Ready() || decision.Reason() != ReasonRemoteAuthFailed || decision.AuthFailure() != AuthFailureRejected {
		t.Fatalf("remote auth decision = %#v", decision)
	}
}

func TestEvaluateRequiresBothLocalMCPChecks(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	for _, test := range []struct {
		name string
		ping CheckState
		info CheckState
		want Reason
	}{
		{name: "ping", ping: CheckNotReady, info: CheckReady, want: ReasonLocalPingNotReady},
		{name: "server info", ping: CheckReady, info: CheckNotReady, want: ReasonLocalServerInfoNotReady},
		{name: "unknown ping", ping: CheckUnknown, info: CheckReady, want: ReasonLocalPingNotReady},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := testSession(t, byte(40+len(test.name)), testConnection, testRevision, 1)
			context := testContext(t, session)
			child, _, remote := testAttestations(t, session, context, now)
			local, err := session.NewLocalMCPAttestation(context, now, AuthAuthenticated, test.ping, test.info)
			if err != nil {
				t.Fatalf("NewLocalMCPAttestation: %v", err)
			}
			decision := testEvaluator(t, ClockFunc(func() time.Time { return now }), time.Minute, 8).Evaluate(context, child, local, remote)
			if decision.Ready() || decision.Reason() != test.want {
				t.Fatalf("decision = %#v, want reason %q", decision, test.want)
			}
		})
	}
}

func TestReplayKeyIsBoundToConnectionRevisionAndGeneration(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	clock := &testClock{now: now}
	evaluator := testEvaluator(t, clock, time.Minute, 4)

	// Identical deterministic entropy gives both issuers identical nonce bytes.
	// Different connection/revision/generation tuples must still have distinct
	// replay keys; issuer pointer identity is not the replay scope.
	issuerA := testIssuer(t, 50)
	issuerB := testIssuer(t, 50)
	sessionA, err := issuerA.NewSession("connection-a", "revision-1", 1)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	sessionB, err := issuerB.NewSession("connection-b", "revision-1", 1)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	contextA := testContext(t, sessionA)
	contextB := testContext(t, sessionB)
	if !contextA.Nonce().Equal(contextB.Nonce()) {
		t.Fatal("test setup did not produce equal nonce bytes")
	}
	childA, localA, remoteA := testAttestations(t, sessionA, contextA, now)
	childB, localB, remoteB := testAttestations(t, sessionB, contextB, now)
	if decision := evaluator.Evaluate(contextA, childA, localA, remoteA); !decision.Ready() {
		t.Fatalf("connection A = %#v", decision)
	}
	if decision := evaluator.Evaluate(contextB, childB, localB, remoteB); !decision.Ready() {
		t.Fatalf("connection B = %#v", decision)
	}

	issuerC := testIssuer(t, 50)
	sessionC, err := issuerC.NewSession("connection-a", "revision-2", 2)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	contextC := testContext(t, sessionC)
	childC, localC, remoteC := testAttestations(t, sessionC, contextC, now)
	if decision := evaluator.Evaluate(contextC, childC, localC, remoteC); !decision.Ready() {
		t.Fatalf("revision/generation C = %#v", decision)
	}
}

func TestReplayEntriesExpireWithFreshnessAndCapacityFailsClosedUntilThenRecovers(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	clock := &testClock{now: now}
	evaluator := testEvaluator(t, clock, 10*time.Second, 1)
	session := testSession(t, 60, testConnection, testRevision, 1)
	firstContext := testContext(t, session)
	child, local, remote := testAttestations(t, session, firstContext, now)
	if decision := evaluator.Evaluate(firstContext, child, local, remote); !decision.Ready() {
		t.Fatalf("first = %#v", decision)
	}

	secondContext := testContext(t, session)
	child, local, remote = testAttestations(t, session, secondContext, now)
	if decision := evaluator.Evaluate(secondContext, child, local, remote); decision.Reason() != ReasonNonceCapacity {
		t.Fatalf("capacity = %#v", decision)
	}

	clock.Set(now.Add(11 * time.Second))
	thirdContext := testContext(t, session)
	child, local, remote = testAttestations(t, session, thirdContext, now.Add(11*time.Second))
	if decision := evaluator.Evaluate(thirdContext, child, local, remote); !decision.Ready() {
		t.Fatalf("after TTL = %#v", decision)
	}
}

func TestReplayEntryIsRetainedAtExactFreshnessBoundary(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	clock := &testClock{now: now}
	evaluator := testEvaluator(t, clock, 10*time.Second, 1)
	session := testSession(t, 61, testConnection, testRevision, 1)
	context := testContext(t, session)
	child, local, remote := testAttestations(t, session, context, now)
	if decision := evaluator.Evaluate(context, child, local, remote); !decision.Ready() {
		t.Fatalf("first = %#v", decision)
	}

	clock.Set(now.Add(10 * time.Second))
	if decision := evaluator.Evaluate(context, child, local, remote); decision.Reason() != ReasonDuplicateNonce {
		t.Fatalf("boundary replay = %#v", decision)
	}
}

func TestReplayCapacityIsIsolatedPerSessionScope(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	evaluator := testEvaluator(t, ClockFunc(func() time.Time { return now }), time.Minute, 1)
	issuer := testIssuer(t, 62)
	sessionA, err := issuer.NewSession("connection-a", testRevision, 1)
	if err != nil {
		t.Fatalf("NewSession A: %v", err)
	}
	sessionB, err := issuer.NewSession("connection-b", testRevision, 1)
	if err != nil {
		t.Fatalf("NewSession B: %v", err)
	}
	for name, session := range map[string]*Session{"A": sessionA, "B": sessionB} {
		context := testContext(t, session)
		child, local, remote := testAttestations(t, session, context, now)
		if decision := evaluator.Evaluate(context, child, local, remote); !decision.Ready() {
			t.Fatalf("session %s = %#v", name, decision)
		}
	}
}

func TestReplayScopeCountHasGlobalBound(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	evaluator, err := New(Options{
		Clock: ClockFunc(func() time.Time { return now }), Freshness: time.Minute,
		NonceLimit: 1, ScopeLimit: 1,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	issuer := testIssuer(t, 64)
	first := testSessionFromIssuer(t, issuer, "connection-a", testRevision, 1)
	second := testSessionFromIssuer(t, issuer, "connection-b", testRevision, 1)
	for index, session := range []*Session{first, second} {
		context := testContext(t, session)
		child, local, remote := testAttestations(t, session, context, now)
		decision := evaluator.Evaluate(context, child, local, remote)
		if index == 0 && !decision.Ready() {
			t.Fatalf("first = %#v", decision)
		}
		if index == 1 && decision.Reason() != ReasonNonceCapacity {
			t.Fatalf("second = %#v", decision)
		}
	}
}

func TestSessionRevokeInvalidatesDerivedValuesAndFutureContexts(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	evaluator := testEvaluator(t, ClockFunc(func() time.Time { return now }), time.Minute, 8)
	session := testSession(t, 63, testConnection, testRevision, 1)
	context := testContext(t, session)
	child, local, remote := testAttestations(t, session, context, now)

	session.Revoke()
	if _, err := session.NewContext(); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("NewContext after revoke error = %v", err)
	}
	if decision := evaluator.Evaluate(context, child, local, remote); decision.Reason() != ReasonInvalidContext {
		t.Fatalf("revoked decision = %#v", decision)
	}
	if session.ConnectionID() != "" || session.Revision() != "" || session.Generation() != 0 {
		t.Fatal("revoked session still exposes a valid identity")
	}
}

func TestReadinessValuesRejectJSON(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	issuer := testIssuer(t, 70)
	session, err := issuer.NewSession(testConnection, testRevision, 1)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	context := testContext(t, session)
	child, local, remote := testAttestations(t, session, context, now)
	evaluator := testEvaluator(t, ClockFunc(func() time.Time { return now }), time.Minute, 8)
	decision := evaluator.Evaluate(context, child, local, remote)

	values := []any{issuer, session, context.Nonce(), context, child, local, remote, decision, evaluator}
	for index, value := range values {
		if _, err := json.Marshal(value); !errors.Is(err, ErrLocalOnly) {
			t.Fatalf("value %d marshal error = %v", index, err)
		}
	}

	for index, value := range []any{&Issuer{}, &Session{}, &Nonce{}, &Context{}, &ChildAttestation{}, &LocalMCPAttestation{}, &RemoteTunnelAttestation{}, &Decision{}, &Evaluator{}} {
		if err := json.Unmarshal([]byte(`{}`), value); !errors.Is(err, ErrLocalOnly) {
			t.Fatalf("value %d unmarshal error = %v", index, err)
		}
	}
}

func TestIssuerEntropyAndSessionValidation(t *testing.T) {
	if _, err := NewIssuerWithEntropy(nil); !errors.Is(err, ErrEntropyUnavailable) {
		t.Fatalf("nil entropy error = %v", err)
	}
	if _, err := NewIssuerWithEntropy(bytes.NewReader(make([]byte, 64))); !errors.Is(err, ErrEntropyUnavailable) {
		t.Fatalf("zero entropy error = %v", err)
	}
	issuer := testIssuer(t, 80)
	if _, err := issuer.NewSession("bad/id", testRevision, 1); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("invalid session error = %v", err)
	}
	if _, err := (*Issuer)(nil).NewSession(testConnection, testRevision, 1); !errors.Is(err, ErrInvalidIssuer) {
		t.Fatalf("nil issuer error = %v", err)
	}
	if _, err := (*Session)(nil).NewContext(); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("nil session context error = %v", err)
	}
}

func TestEvaluatorIsSafeForConcurrentUseAndConsumesOnlyOnce(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	evaluator := testEvaluator(t, ClockFunc(func() time.Time { return now }), time.Minute, 128)
	session := testSession(t, 90, testConnection, testRevision, 1)
	context := testContext(t, session)
	child, local, remote := testAttestations(t, session, context, now)

	const workers = 32
	decisions := make(chan Decision, workers)
	var group sync.WaitGroup
	group.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer group.Done()
			decisions <- evaluator.Evaluate(context, child, local, remote)
		}()
	}
	group.Wait()
	close(decisions)

	ready := 0
	duplicates := 0
	for decision := range decisions {
		switch decision.Reason() {
		case ReasonReady:
			ready++
		case ReasonDuplicateNonce:
			duplicates++
		default:
			t.Fatalf("decision = %#v", decision)
		}
	}
	if ready != 1 || duplicates != workers-1 {
		t.Fatalf("ready=%d duplicates=%d", ready, duplicates)
	}
}

func TestOptionsAreBounded(t *testing.T) {
	if ProductionReady {
		t.Fatal("readiness package unexpectedly claims production readiness")
	}
	for _, test := range []struct {
		name    string
		options Options
		valid   bool
	}{
		{name: "defaulted freshness", options: Options{Freshness: 0, NonceLimit: 1}, valid: true},
		{name: "negative freshness", options: Options{Freshness: -time.Second, NonceLimit: 1}},
		{name: "large freshness", options: Options{Freshness: MaxFreshnessWindow + time.Nanosecond, NonceLimit: 1}},
		{name: "defaulted nonce limit", options: Options{Freshness: time.Second, NonceLimit: 0}, valid: true},
		{name: "large nonce limit", options: Options{Freshness: time.Second, NonceLimit: MaxNonceLimit + 1}},
		{name: "defaulted scope limit", options: Options{Freshness: time.Second, NonceLimit: 1, ScopeLimit: 0}, valid: true},
		{name: "large scope limit", options: Options{Freshness: time.Second, NonceLimit: 1, ScopeLimit: MaxScopeLimit + 1}},
		{name: "excessive replay product", options: Options{Freshness: time.Second, NonceLimit: MaxNonceLimit, ScopeLimit: MaxReplayEntries/MaxNonceLimit + 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(test.options)
			if test.valid && err != nil {
				t.Fatalf("New: %v", err)
			}
			if !test.valid && err == nil {
				t.Fatal("invalid options accepted")
			}
		})
	}
	if _, err := New(Options{Freshness: time.Second, NonceLimit: 1}); err != nil {
		t.Fatalf("valid options: %v", err)
	}
}
