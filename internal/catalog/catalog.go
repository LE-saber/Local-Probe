// Package catalog contains a bounded, local-only metadata candidate catalog.
//
// A catalog is an acceleration cache for already authorized discovery. It
// stores only normalized relative paths and small metadata; it never stores
// file content and it never opens paths. Every result is only a candidate and
// must be live-verified by the caller through the authorized rootfs/search
// path before it is used. A catalog is deliberately fail-closed while its
// scope is stale or its contents are dirty.
package catalog

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/LE-saber/Local-Probe/internal/readcore"
)

const SchemaVersion = "local-probe.catalog.v1"

// ProductionReady remains false: this package is a local acceleration
// contract only and has no production runtime/search wiring or end-to-end
// platform verification.
const ProductionReady = false

var (
	ErrInvalidRequest     = errors.New("catalog invalid request")
	ErrInvalidCursor      = errors.New("catalog invalid cursor")
	ErrDenied             = errors.New("catalog access denied")
	ErrStale              = errors.New("catalog scope or generation is stale")
	ErrScopeMismatch      = errors.New("catalog scope mismatch")
	ErrReconcileRequired  = errors.New("catalog reconcile required")
	ErrLimit              = errors.New("catalog budget exhausted")
	ErrDuplicate          = errors.New("catalog duplicate candidate")
	ErrClosed             = errors.New("catalog closed")
	ErrGenerationOverflow = errors.New("catalog generation exhausted")
	ErrLocalOnly          = errors.New("catalog value is local-only")
)

// Scope is implemented by policy.BoundScope. It is intentionally a local
// typed interface rather than a wire representation. Validate must re-check
// the current configuration revision and revocation state. Implementations
// must not be constructed from model-supplied fields.
//
// The authorization methods are used when candidates enter the catalog. A
// query only uses Validate and the catalog's bound identity; changing policy
// must advance the revision and thereby invalidate the catalog scope.
type Scope interface {
	Validate() error
	ConnectionID() string
	ProfileID() string
	Revision() string
	AllowsRoot(string) bool
	AllowsPath(string, string) bool
}

// Kind is a small, platform-neutral metadata classification. It is not a
// promise that the object still exists or still has this type.
type Kind string

const (
	KindRegular   Kind = "regular"
	KindDirectory Kind = "directory"
	KindSymlink   Kind = "symlink"
	KindReparse   Kind = "reparse"
	KindOther     Kind = "other"
	KindUnknown   Kind = "unknown"
)

// Metadata is deliberately limited to metadata that can be revalidated by a
// live opener. VersionToken is an opaque, bounded token; it is not file
// content and must not be treated as a strong snapshot proof.
type Metadata struct {
	SizeBytes       int64
	ModTimeUnixNano int64
	VersionToken    string
}

// Candidate is the only record type retained by the catalog. Path is always a
// canonical slash-separated path relative to RootID. There is no content,
// absolute path, open handle, or authorization capability in this type.
type Candidate struct {
	RootID   string
	Path     string
	Kind     Kind
	Metadata Metadata
}

// Limits bound both persistent catalog state and the typed query surface. The
// memory limit is an explicit approximate model: it includes conservative
// per-entry map/index overhead and retained string bytes, not Go allocator
// fragmentation. Query results are bounded by MaxPageSize.
type Limits struct {
	MaxEntries               int
	MaxMemoryBytes           int
	MaxPageSize              int
	MaxPathBytes             int
	MaxVersionTokenBytes     int
	MaxSourceGenerationBytes int
}

func DefaultLimits() Limits {
	return Limits{
		MaxEntries:               100_000,
		MaxMemoryBytes:           64 << 20,
		MaxPageSize:              128,
		MaxPathBytes:             4096,
		MaxVersionTokenBytes:     256,
		MaxSourceGenerationBytes: 256,
	}
}

func (l Limits) validate() error {
	if l.MaxEntries < 1 || l.MaxEntries > 1<<20 ||
		l.MaxMemoryBytes < 4<<10 || l.MaxMemoryBytes > 1<<30 ||
		l.MaxPageSize < 1 || l.MaxPageSize > 1024 ||
		l.MaxPathBytes < 1 || l.MaxPathBytes > 4096 ||
		l.MaxVersionTokenBytes < 0 || l.MaxVersionTokenBytes > 4096 ||
		l.MaxSourceGenerationBytes < 1 || l.MaxSourceGenerationBytes > 4096 {
		return fmt.Errorf("%w: catalog limits exceed safety bounds", ErrInvalidRequest)
	}
	return nil
}

// Identity is diagnostic information for a catalog's trusted binding. It is
// not accepted from a model and is not an authorization token.
type Identity struct {
	ConnectionID string
	ProfileID    string
	Revision     string
}

// ReconcileInput replaces the catalog atomically after a complete authorized
// scan. SourceGeneration is an opaque local generation/fingerprint supplied by
// the scanner; it is not used as an existence proof by Query.
type ReconcileInput struct {
	SourceGeneration string
	Candidates       []Candidate
}

// QueryRequest is a local typed API. Cursor is intentionally not JSON
// serializable; it cannot be forged from MCP input and it is invalidated by a
// catalog generation change.
type QueryRequest struct {
	RootID   string
	Prefix   string
	PageSize int
	Cursor   *Cursor
}

// QueryResult contains candidates only. A nil Next means the current catalog
// page sequence is exhausted; it does not prove that the filesystem has no
// additional or changed paths. Callers must live-verify every candidate and
// perform a direct/reconcile scan when complete coverage is required.
type QueryResult struct {
	SchemaVersion    string
	Generation       uint64
	SourceGeneration string
	Candidates       []Candidate
	Next             *Cursor
}

// MarshalJSON and UnmarshalJSON keep the complete candidate result local even
// when the final page has no Cursor. Relying only on Cursor.MarshalJSON would
// otherwise allow a last-page QueryResult to cross a wire boundary.
func (QueryResult) MarshalJSON() ([]byte, error) { return nil, ErrLocalOnly }

func (*QueryResult) UnmarshalJSON([]byte) error { return ErrLocalOnly }

// State exposes bounded diagnostic counters. Dirty means the index must not be
// queried until a successful full Reconcile. Reconciled distinguishes a fresh
// empty catalog from a never-reconciled startup state.
type State struct {
	Generation        uint64
	SourceGeneration  string
	Dirty             bool
	Reconciled        bool
	Entries           int
	ApproxMemoryBytes int
}

// Cursor is intentionally local-only. Its private owner and scope fields
// prevent a cursor copied from another catalog or scope from being accepted.
type Cursor struct {
	owner      *Catalog
	identity   scopeKey
	generation uint64
	rootID     string
	prefix     string
	pageSize   int
	offset     int
}

// MarshalJSON and UnmarshalJSON are explicit local-only gates. In particular,
// private fields would otherwise marshal as an apparently valid `{}` cursor,
// and QueryResult contains Next.
func (Cursor) MarshalJSON() ([]byte, error) { return nil, ErrLocalOnly }

func (*Cursor) UnmarshalJSON([]byte) error { return ErrLocalOnly }

type scopeKey struct {
	connectionID string
	profileID    string
	revision     string
}

type entryKey struct {
	rootID string
	path   string
}

const (
	entryOverheadBytes = 256
	indexOverheadBytes = 32
)

type Catalog struct {
	mu sync.RWMutex

	limits           Limits
	identity         scopeKey
	entries          map[entryKey]Candidate
	order            []entryKey
	usedMemory       int
	generation       uint64
	sourceGeneration string
	dirty            bool
	reconciled       bool
	closed           bool
}

// New creates a catalog bound to one authenticated connection/profile/revision.
// The initial state is dirty and must be reconciled before Query can return
// anything, including an empty result.
func New(scope Scope, limits Limits) (*Catalog, error) {
	if err := limits.validate(); err != nil {
		return nil, err
	}
	identity, err := scopeIdentity(scope)
	if err != nil {
		return nil, err
	}
	return &Catalog{
		limits:     limits,
		identity:   identity,
		entries:    make(map[entryKey]Candidate),
		generation: 1,
		dirty:      true,
	}, nil
}

func (c *Catalog) Identity() Identity {
	if c == nil {
		return Identity{}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return Identity{ConnectionID: c.identity.connectionID, ProfileID: c.identity.profileID, Revision: c.identity.revision}
}

// Reconcile atomically replaces all records. Input validation and budget
// accounting happen before the old catalog is touched. A cancelled or stale
// reconcile therefore leaves the previous state intact and dirty status
// unchanged.
func (c *Catalog) Reconcile(ctx context.Context, scope Scope, input ReconcileInput) error {
	if c == nil {
		return ErrClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.authorize(scope); err != nil {
		return err
	}
	if input.SourceGeneration == "" || !boundedText(input.SourceGeneration, c.limits.MaxSourceGenerationBytes) {
		return fmt.Errorf("%w: source generation", ErrInvalidRequest)
	}
	prepared, ordered, used, err := c.prepareCandidates(ctx, scope, input.Candidates, true)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.validateIdentity(scope); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.validateIdentity(scope); err != nil {
		return err
	}
	if err := c.advanceGenerationLocked(); err != nil {
		return err
	}
	c.entries = prepared
	c.order = ordered
	c.usedMemory = used
	c.sourceGeneration = strings.Clone(input.SourceGeneration)
	c.dirty = false
	c.reconciled = true
	return nil
}

// Upsert adds or replaces candidates atomically. It is suitable for trusted
// local incremental observations, but does not clear Dirty: only Reconcile can
// establish a clean catalog.
func (c *Catalog) Upsert(ctx context.Context, scope Scope, candidates []Candidate) error {
	if c == nil {
		return ErrClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(candidates) == 0 {
		return c.authorize(scope)
	}
	if len(candidates) > c.limits.MaxEntries {
		return ErrLimit
	}
	if err := c.authorize(scope); err != nil {
		return err
	}
	prepared, _, _, err := c.prepareCandidates(ctx, scope, candidates, false)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.validateIdentity(scope); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.validateIdentity(scope); err != nil {
		return err
	}
	newEntries := 0
	newUsed := c.usedMemory
	for key, candidate := range prepared {
		old, exists := c.entries[key]
		if exists {
			if old == candidate {
				continue
			}
			newUsed -= estimateCandidate(old)
		} else {
			newEntries++
			newUsed += indexOverheadBytes
		}
		newUsed += estimateCandidate(candidate)
	}
	if len(c.entries)+newEntries > c.limits.MaxEntries || newUsed < 0 || newUsed > c.limits.MaxMemoryBytes {
		return ErrLimit
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.validateIdentity(scope); err != nil {
		return err
	}
	changed := false
	if c.generation == ^uint64(0) {
		return ErrGenerationOverflow
	}
	for key, candidate := range prepared {
		old, exists := c.entries[key]
		if exists && old == candidate {
			continue
		}
		if !exists {
			c.insertOrderLocked(key)
		}
		c.entries[key] = candidate
		changed = true
	}
	if !changed {
		return nil
	}
	if err := c.advanceGenerationLocked(); err != nil {
		return err
	}
	c.usedMemory = newUsed
	return nil
}

// Remove removes one candidate. Removal is a local cache mutation and does not
// claim that the corresponding filesystem path is absent.
func (c *Catalog) Remove(ctx context.Context, scope Scope, rootID, path string) error {
	if c == nil {
		return ErrClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.authorize(scope); err != nil {
		return err
	}
	if err := validatePath(rootID, path, c.limits); err != nil {
		return err
	}
	if err := authorizeCandidate(scope, rootID, path); err != nil {
		return err
	}
	key := entryKey{rootID: rootID, path: path}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return ErrClosed
	}
	old, ok := c.entries[key]
	if !ok {
		return nil
	}
	if err := c.advanceGenerationLocked(); err != nil {
		return err
	}
	delete(c.entries, key)
	c.removeOrderLocked(key)
	c.usedMemory -= estimateCandidate(old) + indexOverheadBytes
	if c.usedMemory < 0 {
		c.usedMemory = 0
	}
	return nil
}

// MarkDirty invalidates all cursors and makes Query fail closed until a full
// Reconcile succeeds. Watchers may call this on any hint, including overflow;
// they must never use watcher state as proof of complete coverage.
func (c *Catalog) MarkDirty(ctx context.Context, scope Scope) error {
	if c == nil {
		return ErrClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.authorize(scope); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return ErrClosed
	}
	if err := c.advanceGenerationLocked(); err != nil {
		return err
	}
	c.dirty = true
	return nil
}

// Query returns only the bounded page of cached candidates. It cannot return
// while dirty, stale, revoked, closed, or with a cursor from another
// generation/catalog. The final scope check discards a page if revocation is
// observed during the operation.
func (c *Catalog) Query(ctx context.Context, scope Scope, req QueryRequest) (QueryResult, error) {
	out := QueryResult{SchemaVersion: SchemaVersion, Candidates: []Candidate{}}
	if c == nil {
		return out, ErrClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if err := c.authorize(scope); err != nil {
		return out, err
	}
	pageSize := req.PageSize
	if pageSize == 0 {
		pageSize = c.limits.MaxPageSize
	}
	if pageSize < 1 || pageSize > c.limits.MaxPageSize {
		return out, fmt.Errorf("%w: page size", ErrInvalidRequest)
	}
	if err := validateRootID(req.RootID); err != nil {
		return out, err
	}
	if req.Prefix != "" && !readcore.ValidPath(req.Prefix) {
		return out, fmt.Errorf("%w: prefix", ErrInvalidRequest)
	}
	if !scope.AllowsRoot(req.RootID) {
		if scope.Validate() != nil {
			return out, staleScopeError()
		}
		return out, ErrDenied
	}

	c.mu.RLock()
	if c.closed {
		c.mu.RUnlock()
		return out, ErrClosed
	}
	if c.dirty || !c.reconciled {
		c.mu.RUnlock()
		return out, ErrReconcileRequired
	}
	if err := c.validateCursorLocked(req, pageSize); err != nil {
		c.mu.RUnlock()
		return out, err
	}
	offset := 0
	if req.Cursor != nil {
		offset = req.Cursor.offset
	}
	out.Generation = c.generation
	out.SourceGeneration = c.sourceGeneration
	nextOffset := offset
	for nextOffset < len(c.order) {
		if err := ctx.Err(); err != nil {
			c.mu.RUnlock()
			return QueryResult{SchemaVersion: SchemaVersion, Candidates: []Candidate{}}, err
		}
		key := c.order[nextOffset]
		nextOffset++
		if key.rootID != req.RootID || !pathHasPrefix(key.path, req.Prefix) {
			continue
		}
		out.Candidates = append(out.Candidates, c.entries[key])
		if len(out.Candidates) >= pageSize {
			break
		}
	}
	if nextOffset < len(c.order) {
		out.Next = &Cursor{owner: c, identity: c.identity, generation: c.generation, rootID: req.RootID, prefix: req.Prefix, pageSize: pageSize, offset: nextOffset}
	}
	c.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return QueryResult{SchemaVersion: SchemaVersion, Candidates: []Candidate{}}, err
	}
	for _, candidate := range out.Candidates {
		if err := ctx.Err(); err != nil {
			return QueryResult{SchemaVersion: SchemaVersion, Candidates: []Candidate{}}, err
		}
		if scope.AllowsPath(candidate.RootID, candidate.Path) {
			continue
		}
		if scope.Validate() != nil {
			return QueryResult{SchemaVersion: SchemaVersion, Candidates: []Candidate{}}, staleScopeError()
		}
		return QueryResult{SchemaVersion: SchemaVersion, Candidates: []Candidate{}}, ErrDenied
	}
	if err := c.authorize(scope); err != nil {
		return QueryResult{SchemaVersion: SchemaVersion, Candidates: []Candidate{}}, err
	}
	return out, nil
}

func (c *Catalog) validateCursorLocked(req QueryRequest, pageSize int) error {
	if req.Cursor == nil {
		return nil
	}
	cur := req.Cursor
	if cur.owner != c || cur.identity != c.identity || cur.generation != c.generation ||
		cur.rootID != req.RootID || cur.prefix != req.Prefix || cur.pageSize != pageSize ||
		cur.offset < 0 || cur.offset > len(c.order) {
		return ErrInvalidCursor
	}
	return nil
}

// Stats returns bounded state without returning any candidate paths.
func (c *Catalog) Stats(scope Scope) (State, error) {
	if c == nil {
		return State{}, ErrClosed
	}
	if err := c.authorize(scope); err != nil {
		return State{}, err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.closed {
		return State{}, ErrClosed
	}
	return State{Generation: c.generation, SourceGeneration: c.sourceGeneration, Dirty: c.dirty, Reconciled: c.reconciled, Entries: len(c.entries), ApproxMemoryBytes: c.usedMemory}, nil
}

// Close irreversibly clears the catalog. It is scoped so a stale or foreign
// connection cannot close another connection's cache accidentally.
func (c *Catalog) Close(scope Scope) error {
	if c == nil {
		return nil
	}
	if err := c.authorize(scope); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	if err := c.advanceGenerationLocked(); err != nil {
		return err
	}
	c.closed = true
	c.entries = nil
	c.order = nil
	c.usedMemory = 0
	c.dirty = true
	return nil
}

func (c *Catalog) authorize(scope Scope) error {
	key, err := scopeIdentity(scope)
	if err != nil {
		return err
	}
	c.mu.RLock()
	closed := c.closed
	identity := c.identity
	c.mu.RUnlock()
	if closed {
		return ErrClosed
	}
	if key.connectionID != identity.connectionID || key.profileID != identity.profileID {
		return ErrScopeMismatch
	}
	if key.revision != identity.revision {
		return errors.Join(ErrDenied, ErrStale)
	}
	return nil
}

// validateIdentity repeats the external scope check without taking c.mu. It
// is used immediately before and inside a commit lock so cancellation and
// revocation observed while waiting for the lock cannot commit a prepared
// snapshot. c.identity is immutable for the lifetime of a Catalog.
func (c *Catalog) validateIdentity(scope Scope) error {
	key, err := scopeIdentity(scope)
	if err != nil {
		return err
	}
	if key.connectionID != c.identity.connectionID || key.profileID != c.identity.profileID {
		return ErrScopeMismatch
	}
	if key.revision != c.identity.revision {
		return errors.Join(ErrDenied, ErrStale)
	}
	return nil
}

func scopeIdentity(scope Scope) (scopeKey, error) {
	if scope == nil {
		return scopeKey{}, staleScopeError()
	}
	if err := scope.Validate(); err != nil {
		return scopeKey{}, staleScopeError()
	}
	key := scopeKey{connectionID: scope.ConnectionID(), profileID: scope.ProfileID(), revision: scope.Revision()}
	if !validID(key.connectionID) || !validID(key.profileID) || !boundedText(key.revision, 4096) {
		return scopeKey{}, fmt.Errorf("%w: scope identity", ErrInvalidRequest)
	}
	return key, nil
}

func staleScopeError() error { return errors.Join(ErrDenied, ErrStale) }

func authorizeCandidate(scope Scope, rootID, path string) error {
	if !scope.AllowsRoot(rootID) || !scope.AllowsPath(rootID, path) {
		if scope.Validate() != nil {
			return staleScopeError()
		}
		return ErrDenied
	}
	return nil
}

func (c *Catalog) prepareCandidates(ctx context.Context, scope Scope, candidates []Candidate, rejectDuplicates bool) (map[entryKey]Candidate, []entryKey, int, error) {
	if len(candidates) > c.limits.MaxEntries {
		return nil, nil, 0, ErrLimit
	}
	prepared := make(map[entryKey]Candidate, len(candidates))
	used := 0
	for i, candidate := range candidates {
		if i%64 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, nil, 0, err
			}
		}
		candidate, err := normalizeCandidate(candidate, c.limits)
		if err != nil {
			return nil, nil, 0, err
		}
		if err := authorizeCandidate(scope, candidate.RootID, candidate.Path); err != nil {
			return nil, nil, 0, err
		}
		key := entryKey{rootID: candidate.RootID, path: candidate.Path}
		if _, exists := prepared[key]; exists {
			if rejectDuplicates {
				return nil, nil, 0, ErrDuplicate
			}
			return nil, nil, 0, ErrDuplicate
		}
		prepared[key] = candidate
		entryBytes := estimateCandidate(candidate) + indexOverheadBytes
		if used > c.limits.MaxMemoryBytes-entryBytes {
			return nil, nil, 0, ErrLimit
		}
		used += entryBytes
	}
	ordered := make([]entryKey, 0, len(prepared))
	for key := range prepared {
		ordered = append(ordered, key)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].rootID != ordered[j].rootID {
			return ordered[i].rootID < ordered[j].rootID
		}
		return ordered[i].path < ordered[j].path
	})
	return prepared, ordered, used, nil
}

func normalizeCandidate(candidate Candidate, limits Limits) (Candidate, error) {
	if err := validatePath(candidate.RootID, candidate.Path, limits); err != nil {
		return Candidate{}, err
	}
	switch candidate.Kind {
	case KindRegular, KindDirectory, KindSymlink, KindReparse, KindOther, KindUnknown:
	default:
		return Candidate{}, fmt.Errorf("%w: candidate kind", ErrInvalidRequest)
	}
	if candidate.Metadata.SizeBytes < 0 || !boundedText(candidate.Metadata.VersionToken, limits.MaxVersionTokenBytes) {
		return Candidate{}, fmt.Errorf("%w: candidate metadata", ErrInvalidRequest)
	}
	// Clone all retained strings so the catalog owns its bounded record bytes.
	candidate.RootID = strings.Clone(candidate.RootID)
	candidate.Path = strings.Clone(candidate.Path)
	candidate.Kind = Kind(strings.Clone(string(candidate.Kind)))
	candidate.Metadata.VersionToken = strings.Clone(candidate.Metadata.VersionToken)
	return candidate, nil
}

func validatePath(rootID, path string, limits Limits) error {
	if validateRootID(rootID) != nil || len(path) > limits.MaxPathBytes || !readcore.ValidPath(path) {
		return fmt.Errorf("%w: candidate path", ErrInvalidRequest)
	}
	return nil
}

func validateRootID(rootID string) error {
	if !validID(rootID) {
		return fmt.Errorf("%w: root id", ErrInvalidRequest)
	}
	return nil
}

func validID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func boundedText(value string, max int) bool {
	return len(value) <= max && utf8.ValidString(value)
}

func pathHasPrefix(path, prefix string) bool {
	if prefix == "" {
		return true
	}
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

func estimateCandidate(candidate Candidate) int {
	// This is intentionally conservative and documented as approximate. The
	// map stores the key and value, and order stores another pair of string
	// headers; string bytes are counted twice to cover those retained views.
	n := entryOverheadBytes
	for _, length := range []int{len(candidate.RootID), len(candidate.Path), len(candidate.Kind), len(candidate.Metadata.VersionToken)} {
		if length > int(^uint(0)>>1)-n {
			return int(^uint(0) >> 1)
		}
		n += length
	}
	for _, length := range []int{len(candidate.RootID), len(candidate.Path), len(candidate.Metadata.VersionToken)} {
		if length > (int(^uint(0)>>1)-n)/2 {
			return int(^uint(0) >> 1)
		}
		n += 2 * length
	}
	return n
}

func (c *Catalog) advanceGenerationLocked() error {
	if c.generation == ^uint64(0) {
		return ErrGenerationOverflow
	}
	c.generation++
	return nil
}

func (c *Catalog) insertOrderLocked(key entryKey) {
	idx := sort.Search(len(c.order), func(i int) bool {
		if c.order[i].rootID != key.rootID {
			return c.order[i].rootID >= key.rootID
		}
		return c.order[i].path >= key.path
	})
	c.order = append(c.order, entryKey{})
	copy(c.order[idx+1:], c.order[idx:])
	c.order[idx] = key
}

func (c *Catalog) removeOrderLocked(key entryKey) {
	idx := sort.Search(len(c.order), func(i int) bool {
		if c.order[i].rootID != key.rootID {
			return c.order[i].rootID >= key.rootID
		}
		return c.order[i].path >= key.path
	})
	if idx < len(c.order) && c.order[idx] == key {
		copy(c.order[idx:], c.order[idx+1:])
		c.order = c.order[:len(c.order)-1]
	}
}
