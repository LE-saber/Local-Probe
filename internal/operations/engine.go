package operations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/LE-saber/Local-Probe/internal/config"
	"github.com/LE-saber/Local-Probe/internal/policy"
	"github.com/LE-saber/Local-Probe/internal/probe"
	"github.com/LE-saber/Local-Probe/internal/rootfs"
)

type Options struct {
	Journal   *Journal
	Config    *config.Store
	Manager   *policy.Manager
	Workspace Workspace
	Catalog   Catalog
	// Acquire must resolve the authenticated subject again and hold a cancellable
	// identity lease until release. It is supplied by the target's peer registry.
	Acquire func(context.Context, Subject) (context.Context, func(), error)
}
type runner struct {
	tool       Tool
	descriptor probe.ExecutableDescriptor
}
type runningJob struct {
	cancel context.CancelFunc
	done   chan struct{}
	until  time.Time
}
type Engine struct {
	mu        sync.Mutex
	opts      Options
	runners   map[string]runner
	jobs      map[string]Record
	running   map[string]*runningJob
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	wg        sync.WaitGroup
	lock      fsLock
	unhealthy bool
	epoch     string
}

func (e *Engine) Allows(s Subject) bool {
	_, err := e.authorize(s)
	e.mu.Lock()
	defer e.mu.Unlock()
	return err == nil && !e.unhealthy && e.ctx.Err() == nil
}

// Tools returns only logical runner identities, never executable paths or env.
func (e *Engine) Tools() []struct {
	ID         string `json:"id"`
	SHA256     string `json:"sha256"`
	ScriptType string `json:"script_type,omitempty"`
} {
	out := make([]struct {
		ID         string `json:"id"`
		SHA256     string `json:"sha256"`
		ScriptType string `json:"script_type,omitempty"`
	}, 0, len(e.runners))
	for _, r := range e.runners {
		out = append(out, struct {
			ID         string `json:"id"`
			SHA256     string `json:"sha256"`
			ScriptType string `json:"script_type,omitempty"`
		}{r.tool.ID, r.tool.SHA256, r.tool.ScriptType})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func New(ctx context.Context, o Options) (*Engine, error) {
	if ctx == nil || o.Journal == nil || o.Config == nil || o.Manager == nil || o.Workspace == nil || o.Acquire == nil || o.Catalog.Version != Version || len(o.Catalog.Tools) > 32 {
		return nil, ErrInvalid
	}
	encoded, err := json.Marshal(o.Catalog)
	if err != nil {
		return nil, ErrInvalid
	}
	var cloned Catalog
	if json.Unmarshal(encoded, &cloned) != nil {
		return nil, ErrInvalid
	}
	o.Catalog = cloned
	// Local operation state must never be reachable through an authorized root.
	state, err := filepath.EvalSymlinks(o.Journal.Path)
	if err != nil {
		return nil, ErrStorage
	}
	for _, r := range o.Config.Snapshot().Config().Roots() {
		root, err := filepath.EvalSymlinks(r.Path())
		if err != nil {
			return nil, ErrStorage
		}
		if contained(root, state) || contained(state, root) {
			return nil, ErrDenied
		}
	}
	f, err := o.Journal.root.OpenFile("engine.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, ErrStorage
	}
	e := &Engine{opts: o, runners: map[string]runner{}, running: map[string]*runningJob{}, done: make(chan struct{}), lock: fsLock{file: f}}
	if lockEngine(&e.lock) != nil {
		f.Close()
		return nil, ErrConflict
	}
	ok := false
	defer func() {
		if !ok {
			f.Close()
		}
	}()
	for _, t := range o.Catalog.Tools {
		if t.ID == "" || len(t.ID) > 64 || e.runners[t.ID].tool.ID != "" || !validID(t.SHA256) || len(t.Environment) > 64 {
			return nil, ErrInvalid
		}
		if t.ScriptType != "" && t.ScriptType != "python" && t.ScriptType != "node" && t.ScriptType != "powershell" && t.ScriptType != "cmd" {
			return nil, ErrInvalid
		}
		for _, v := range t.Environment {
			if !strings.Contains(v, "=") || strings.ContainsRune(v, 0) || len(v) > 8192 {
				return nil, ErrInvalid
			}
		}
		d, err := probe.AuditExecutable(probe.ToolVersionGeneric, t.Executable)
		if err != nil {
			return nil, err
		}
		if h, err := hashFile(t.Executable); err != nil || h != t.SHA256 {
			return nil, probe.ErrHashMismatch
		}
		t.Environment = append([]string(nil), t.Environment...)
		e.runners[t.ID] = runner{t, d}
	}
	e.epoch = e.currentEpoch()
	e.jobs, err = o.Journal.Records()
	if err != nil {
		return nil, err
	}
	if len(e.jobs) > 128 {
		return nil, ErrCapacity
	}
	for id, r := range e.jobs {
		if !terminal(r.Summary.State) {
			state := "interrupted"
			if r.Summary.State == "running" {
				state = "unknown"
			}
			if err = e.transition(&r, state, "target_restarted"); err != nil {
				return nil, err
			}
			e.jobs[id] = r
		}
	}
	e.ctx, e.cancel = context.WithCancel(ctx)
	ok = true
	go e.loop()
	return e, nil
}
func contained(parent, child string) bool {
	r, err := filepath.Rel(strings.ToLower(parent), strings.ToLower(child))
	return err == nil && (r == "." || r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)))
}
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", ErrStorage
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, (512<<20)+1))
	if err != nil || n > 512<<20 {
		return "", ErrStorage
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func (e *Engine) currentEpoch() string {
	snapshot := e.opts.Config.Snapshot()
	return digest(struct {
		Config   config.Config
		Catalog  Catalog
		Revision string
	}{snapshot.Config(), e.opts.Catalog, snapshot.Revision()})
}
func (e *Engine) authorize(s Subject) (policy.BoundScope, error) {
	if s.Principal == "" || len(s.Principal) > 256 || s.Connection == "" {
		return policy.BoundScope{}, ErrDenied
	}
	cfg := e.opts.Config.Snapshot().Config()
	c, ok := cfg.Connection(s.Connection)
	if !ok || !c.Enabled() || c.Purpose() != config.PurposeAgentRemote || c.ExecutionPolicy() != config.ExecutionTrustedHost || !cfg.DeveloperMode().AllowsConnection(c.ID()) {
		return policy.BoundScope{}, ErrDenied
	}
	profile, ok := cfg.Profile(c.ProfileID())
	if !ok || profile.ReadOnly() {
		return policy.BoundScope{}, ErrDenied
	}
	b, err := e.opts.Manager.BindAuthenticated(c.ID())
	if err != nil {
		return policy.BoundScope{}, ErrDenied
	}
	return b, nil
}
func validRequest(r Request) bool {
	if r.Nonce == "" || len(r.Nonce) > 128 || !utf8.ValidString(r.Nonce) || strings.ContainsRune(r.Nonce, 0) || r.RootID == "" || len(r.Args) > 64 || len(r.Script) > MaxContent || len(r.Content) > MaxContent || r.TimeoutSeconds < 1 || r.TimeoutSeconds > 1800 {
		return false
	}
	bytes := 0
	for _, s := range r.Args {
		bytes += len(s)
		if len(s) > 4096 || strings.ContainsRune(s, 0) || !utf8.ValidString(s) {
			return false
		}
	}
	if bytes > 16384 || !utf8.ValidString(r.Script) || !utf8.ValidString(r.Content) || strings.ContainsRune(r.Script, 0) {
		return false
	}
	switch r.Kind {
	case "command", "build", "test", "version":
		return r.ToolID != "" && r.Script == "" && r.Path == "" && r.Content == "" && len(r.Edits) == 0
	case "script":
		return r.ToolID != "" && r.Script != "" && r.Path == "" && r.Content == "" && len(r.Edits) == 0
	case "file_write":
		return r.Path != "" && r.ToolID == "" && r.Script == "" && len(r.Args) == 0 && r.Directory == "" && len(r.Edits) == 0
	case "file_patch":
		if r.Path == "" || r.ToolID != "" || r.Script != "" || len(r.Args) != 0 || r.Directory != "" || r.Content != "" || len(r.Edits) == 0 || len(r.Edits) > 32 {
			return false
		}
		size := 0
		for _, edit := range r.Edits {
			if edit.Old == "" || !utf8.ValidString(edit.Old) || !utf8.ValidString(edit.New) {
				return false
			}
			size += len(edit.Old) + len(edit.New)
		}
		return size <= MaxContent
	}
	return false
}
func (e *Engine) Prepare(ctx context.Context, s Subject, r Request) (Summary, error) {
	if r.TimeoutSeconds == 0 {
		r.TimeoutSeconds = 60
	}
	if !validRequest(r) {
		return Summary{}, ErrInvalid
	}
	b, err := e.authorize(s)
	if err != nil {
		return Summary{}, err
	}
	if !b.AllowsRoot(r.RootID) || !b.AllowsDirectory(r.RootID, r.Directory) {
		return Summary{}, ErrDenied
	}
	r.Args = append([]string(nil), r.Args...)
	r.Edits = append([]Edit(nil), r.Edits...)
	id := digest(struct {
		Subject Subject
		Nonce   string
	}{s, r.Nonce})
	e.mu.Lock()
	old, exists := e.jobs[id]
	unhealthy := e.unhealthy
	e.mu.Unlock()
	if unhealthy {
		return Summary{}, ErrStorage
	}
	if exists {
		if digest(old.Plan.Request) != digest(r) {
			return Summary{}, ErrConflict
		}
		return old.Summary, nil
	}
	lease, release, err := e.opts.Acquire(ctx, s)
	if err != nil {
		return Summary{}, ErrDenied
	}
	defer release()
	if lease.Err() != nil {
		return Summary{}, ErrDenied
	}
	r.Args = append([]string(nil), r.Args...)
	p := Plan{Subject: s, Request: r, Epoch: e.currentEpoch()}
	if r.Kind == "file_write" || r.Kind == "file_patch" {
		var before []byte
		p.ExpectedFile, before, err = e.opts.Workspace.FilePreview(b, r.RootID, r.Path)
		if err == nil && !utf8.Valid(before) {
			return Summary{}, ErrInvalid
		}
		p.Before = string(before)
		if err == nil && r.Kind == "file_patch" {
			if p.ExpectedFile == "absent" {
				return Summary{}, ErrConflict
			}
			patched := p.Before
			for _, edit := range r.Edits {
				if strings.Count(patched, edit.Old) != 1 {
					return Summary{}, ErrConflict
				}
				patched = strings.Replace(patched, edit.Old, edit.New, 1)
				if len(patched) > MaxContent {
					return Summary{}, ErrInvalid
				}
			}
			p.PatchedContent = patched
		}
	} else {
		t, ok := e.runners[r.ToolID]
		if !ok || r.Kind == "script" && t.tool.ScriptType == "" {
			return Summary{}, ErrDenied
		}
		// cmd uses a shell parser rather than the Windows CRT argv grammar.
		// This increment accepts a script body, not additional shell arguments
		// or a private script path requiring cmd-specific escaping.
		if r.Kind == "script" && t.tool.ScriptType == "cmd" && (len(r.Args) != 0 || strings.ContainsAny(e.opts.Journal.Path, " \t\r\n\"&|<>^()%!")) {
			return Summary{}, ErrInvalid
		}
		p.ExecutableHash = t.tool.SHA256
		err = e.opts.Workspace.WithDirectory(b, r.RootID, r.Directory, func(string) error { return nil })
	}
	if err != nil {
		return Summary{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.unhealthy || e.ctx.Err() != nil {
		return Summary{}, ErrStorage
	}
	if old, ok := e.jobs[id]; ok {
		if digest(old.Plan.Request) != digest(r) {
			return Summary{}, ErrConflict
		}
		return old.Summary, nil
	}
	if len(e.jobs) >= 128 {
		return Summary{}, ErrCapacity
	}
	now := time.Now().UTC()
	summary := Summary{ID: id, Digest: digest(p), State: "prepared", Kind: r.Kind, RootID: r.RootID, ToolID: r.ToolID, Created: now, Updated: now, Expires: now.Add(5 * time.Minute), ExitCode: -1}
	job := Record{Version: Version, Summary: summary, Plan: p, Sequence: 1}
	encoded, encodeErr := json.Marshal(job)
	if encodeErr != nil || len(encoded) > maxRecord-(256<<10) { // reserve bounded result bytes and terminal metadata
		return Summary{}, ErrInvalid
	}
	if err := e.opts.Journal.save(job); err != nil {
		if errors.Is(err, ErrCapacity) {
			return Summary{}, ErrCapacity
		}
		e.failStorage()
		return Summary{}, ErrStorage
	}
	e.jobs[id] = job
	return summary, nil
}
func (e *Engine) Submit(s Subject, id, expected string) (Summary, error) {
	if !validID(id) || !validID(expected) {
		return Summary{}, ErrInvalid
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.jobs[id]
	if !ok || r.Plan.Subject != s {
		return Summary{}, ErrDenied
	}
	if r.Summary.Digest != expected {
		return Summary{}, ErrConflict
	}
	if _, err := e.authorize(s); err != nil || r.Plan.Epoch != e.currentEpoch() {
		return Summary{}, ErrDenied
	}
	if e.unhealthy || e.ctx.Err() != nil {
		return Summary{}, ErrStorage
	}
	if r.Summary.State == "prepared" {
		if err := e.transition(&r, "awaiting_approval", ""); err != nil {
			return Summary{}, err
		}
		e.jobs[id] = r
	}
	return r.Summary, nil
}
func (e *Engine) Status(s Subject, id string) (Summary, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.jobs[id]
	if !ok || r.Plan.Subject != s {
		return Summary{}, ErrDenied
	}
	if e.unhealthy {
		return Summary{}, ErrStorage
	}
	if live := e.running[id]; live != nil {
		if time.Now().After(live.until) {
			live.cancel()
		} else {
			live.until = time.Now().Add(60 * time.Second)
		}
	}
	return r.Summary, nil
}
func (e *Engine) Output(s Subject, id, stream string, offset, limit int) (Output, error) {
	if offset < 0 || limit < 1 || limit > 32768 {
		return Output{}, ErrInvalid
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.jobs[id]
	if !ok || r.Plan.Subject != s {
		return Output{}, ErrDenied
	}
	var b []byte
	switch stream {
	case "stdout":
		b = r.Stdout
	case "stderr":
		b = r.Stderr
	default:
		return Output{}, ErrInvalid
	}
	if offset > len(b) {
		return Output{}, ErrInvalid
	}
	end := min(len(b), offset+limit)
	return Output{append([]byte(nil), b[offset:end]...), end, len(b)}, nil
}
func (e *Engine) Cancel(ctx context.Context, s Subject, id string) (Summary, error) {
	e.mu.Lock()
	r, ok := e.jobs[id]
	if !ok || r.Plan.Subject != s {
		e.mu.Unlock()
		return Summary{}, ErrDenied
	}
	live := e.running[id]
	if live != nil {
		live.cancel()
		e.mu.Unlock()
		select {
		case <-live.done:
			return e.Status(s, id)
		case <-ctx.Done():
			return Summary{}, ctx.Err()
		}
	}
	if !terminal(r.Summary.State) {
		if err := e.transition(&r, "cancelled", ""); err != nil {
			e.mu.Unlock()
			return Summary{}, err
		}
		e.jobs[id] = r
	}
	e.mu.Unlock()
	return r.Summary, nil
}
func (e *Engine) transition(r *Record, state, code string) error {
	if e.unhealthy {
		return ErrStorage
	}
	next := *r
	next.Sequence++
	next.Summary.State = state
	next.Summary.ErrorCode = code
	next.Summary.Updated = time.Now().UTC()
	if err := e.opts.Journal.save(next); err != nil {
		e.failStorage()
		return ErrStorage
	}
	*r = next
	return nil
}

// All callers hold mu. Storage uncertainty stops every live task and admission.
func (e *Engine) failStorage() {
	e.unhealthy = true
	for _, job := range e.running {
		job.cancel()
	}
}
func (e *Engine) loop() {
	defer close(e.done)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
			e.advance()
		}
	}
}
func (e *Engine) advance() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.unhealthy {
		return
	}
	now := time.Now()
	epoch := e.currentEpoch()
	ids := make([]string, 0, len(e.jobs))
	for id := range e.jobs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return e.jobs[ids[i]].Summary.Created.Before(e.jobs[ids[j]].Summary.Created) })
	for _, id := range ids {
		r := e.jobs[id]
		if terminal(r.Summary.State) {
			continue
		}
		localCancel, err := e.opts.Journal.localCancel(r)
		if err != nil {
			e.failStorage()
			return
		}
		if localCancel {
			if live := e.running[id]; live != nil {
				live.cancel()
			} else {
				if e.transition(&r, "cancelled", "local_owner_cancelled") != nil {
					return
				}
				e.jobs[id] = r
			}
			continue
		}
		_, authErr := e.authorize(r.Plan.Subject)
		valid := authErr == nil && r.Plan.Epoch == epoch
		if live := e.running[id]; live != nil {
			if !valid || now.After(live.until) {
				live.cancel()
			}
			continue
		}
		if !valid || now.After(r.Summary.Expires) {
			state, code := "expired", "approval_expired"
			if !valid {
				state, code = "cancelled", "authorization_changed"
			}
			if e.transition(&r, state, code) == nil {
				e.jobs[id] = r
			}
			continue
		}
		if r.Summary.State == "awaiting_approval" {
			d, present, err := e.opts.Journal.decision(id)
			if err != nil {
				e.failStorage()
				return
			}
			if !present {
				continue
			}
			if d.Version != Version || d.ID != id || d.Digest != r.Summary.Digest || d.Expires != r.Summary.Expires || now.After(d.Expires) {
				e.failStorage()
				return
			}
			state := "rejected"
			if d.Approved && d.TrustedHostAcknowledged {
				state = "queued"
			}
			if e.transition(&r, state, "") != nil {
				return
			}
			e.jobs[id] = r
		}
		if r.Summary.State != "queued" || len(e.running) >= 2 {
			continue
		}
		busy := false
		for active := range e.running {
			if e.jobs[active].Plan.Subject.Connection == r.Plan.Subject.Connection {
				busy = true
			}
		}
		if busy {
			continue
		}
		if e.transition(&r, "running", "") != nil {
			return
		}
		e.jobs[id] = r
		ctx, cancel := context.WithTimeout(e.ctx, time.Duration(r.Plan.Request.TimeoutSeconds)*time.Second)
		live := &runningJob{cancel: cancel, done: make(chan struct{}), until: now.Add(60 * time.Second)}
		e.running[id] = live
		e.wg.Add(1)
		go e.execute(ctx, r, live)
	}
}
func (e *Engine) execute(ctx context.Context, r Record, live *runningJob) {
	defer e.wg.Done()
	defer live.cancel()
	lease, release, err := e.opts.Acquire(ctx, r.Plan.Subject)
	if err == nil {
		defer release()
	}
	var stdout, stderr []byte
	exit := int64(-1)
	if err == nil {
		b, ae := e.authorize(r.Plan.Subject)
		err = ae
		if err == nil && r.Plan.Epoch != e.currentEpoch() {
			err = ErrDenied
		}
		if err == nil {
			request := r.Plan.Request
			if request.Kind == "file_write" || request.Kind == "file_patch" {
				content := request.Content
				if request.Kind == "file_patch" {
					content = r.Plan.PatchedContent
				}
				err = e.opts.Workspace.WriteFile(b, request.RootID, request.Path, r.Plan.ExpectedFile, []byte(content), func(old []byte) error {
					if lease.Err() != nil {
						return lease.Err()
					}
					err := e.opts.Journal.write(r.Summary.ID+".before", struct {
						ID, Digest string
						Data       []byte
					}{r.Summary.ID, r.Summary.Digest, old})
					if err != nil {
						return err
					}
					return lease.Err()
				})
				if err == nil {
					exit = 0
				}
			} else {
				tool := e.runners[request.ToolID]
				var result probe.TrustedProcessResult
				err = e.opts.Workspace.WithDirectory(b, request.RootID, request.Directory, func(cwd string) error {
					if lease.Err() != nil {
						return lease.Err()
					}
					args := append([]string(nil), request.Args...)
					work, er := os.MkdirTemp(e.opts.Journal.Path, "run-")
					if er != nil {
						return ErrStorage
					}
					defer os.RemoveAll(work)
					if request.Kind == "script" {
						var prefix []string
						extension := ".txt"
						switch tool.tool.ScriptType {
						case "python":
							extension = ".py"
						case "node":
							extension = ".js"
						case "powershell":
							extension = ".ps1"
							prefix = []string{"-NoProfile", "-NonInteractive", "-File"}
						case "cmd":
							extension = ".cmd"
							prefix = []string{"/d", "/s", "/c"}
						default:
							return ErrInvalid
						}
						path := filepath.Join(work, "approved"+extension)
						if er = os.WriteFile(path, []byte(request.Script), 0600); er != nil {
							return ErrStorage
						}
						args = append(append(prefix, path), args...)
					}
					env := append([]string(nil), tool.tool.Environment...)
					env = append(env, "TEMP="+work, "TMP="+work)
					result, er = probe.RunTrustedProcess(lease, probe.TrustedProcessSpec{Executable: tool.descriptor, SHA256: tool.tool.SHA256, Args: args, Directory: cwd, Environment: env, Timeout: time.Duration(request.TimeoutSeconds) * time.Second, OutputBytes: 64 << 10, AcquireLaunch: b.AcquireRevisionLease})
					return er
				})
				stdout, stderr, exit = result.Stdout, result.Stderr, result.ExitCode
			}
		}
	}
	state, code := "succeeded", ""
	if err != nil {
		state, code = "failed", "execution_failed"
		var probeError *probe.Error
		if errors.As(err, &probeError) {
			code = string(probeError.Code)
		}
		if errors.Is(err, rootfs.ErrDenied) {
			code = "workspace_denied"
		}
		switch {
		case errors.Is(err, probe.ErrCleanupUnconfirmed):
			state, code = "unknown", "process_tree_cleanup_unconfirmed"
		case errors.Is(err, context.Canceled), errors.Is(err, probe.ErrCancelled):
			state, code = "cancelled", "cancelled"
		case errors.Is(err, context.DeadlineExceeded), errors.Is(err, probe.ErrDeadlineExceeded):
			code = "deadline_exceeded"
		case errors.Is(err, rootfs.ErrWriteConflict):
			code = "file_conflict"
		case errors.Is(err, rootfs.ErrWriteUncertain):
			state, code = "unknown", "write_uncertain"
		case errors.Is(err, probe.ErrOutputLimit):
			code = "output_limit"
		case errors.Is(err, ErrDenied):
			code = "authorization_changed"
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	r.Stdout = stdout
	r.Stderr = stderr
	r.Summary.ExitCode = exit
	r.Summary.StdoutBytes = len(stdout)
	r.Summary.StderrBytes = len(stderr)
	if e.transition(&r, state, code) == nil {
		e.jobs[r.Summary.ID] = r
	}
	if errors.Is(err, probe.ErrCleanupUnconfirmed) {
		e.failStorage()
	}
	delete(e.running, r.Summary.ID)
	close(live.done)
}
func (e *Engine) Close() error { e.cancel(); <-e.done; e.wg.Wait(); return e.lock.file.Close() }
