// Package lifecycleadapter binds one supervisor child generation to an
// admission lifecycle capability. It performs no process, MCP, or tunnel I/O.
package lifecycleadapter

import (
	"context"
	"errors"
	"sync"

	"github.com/LE-saber/Local-Probe/internal/admission"
	"github.com/LE-saber/Local-Probe/internal/supervisor"
)

const ProductionReady = false

var (
	ErrInvalidConfig       = errors.New("invalid lifecycle adapter configuration")
	ErrCancelled           = errors.New("lifecycle adapter cancelled")
	ErrLifecycleBegin      = errors.New("lifecycle begin failed")
	ErrLifecycleMismatch   = errors.New("lifecycle binding mismatch")
	ErrLifecycleActivation = errors.New("lifecycle activation failed")
	ErrRuntimeStart        = errors.New("runtime start failed")
	ErrRuntimeStop         = errors.New("runtime stop failed")
)

// Factory wraps a trusted RuntimeFactory. Every start receives a new exact
// admission capability before the underlying runtime is created.
type Factory struct {
	gate *admission.Gate
	base supervisor.RuntimeFactory
}

func New(gate *admission.Gate, base supervisor.RuntimeFactory) (*Factory, error) {
	if gate == nil || base == nil {
		return nil, ErrInvalidConfig
	}
	return &Factory{gate: gate, base: base}, nil
}

func (f *Factory) Start(ctx context.Context, spec supervisor.ConnectionSpec) (supervisor.Child, error) {
	if f == nil || f.gate == nil || f.base == nil || ctx == nil || spec.Validate() != nil {
		return nil, ErrInvalidConfig
	}
	if ctx.Err() != nil {
		return nil, ErrCancelled
	}
	capability, err := f.gate.BeginLifecycle(spec.ID())
	if err != nil {
		return nil, ErrLifecycleBegin
	}
	if ctx.Err() != nil {
		_ = invalidate(f.gate, capability)
		return nil, ErrCancelled
	}
	binding, err := admission.NewLifecycleBinding(capability)
	if err != nil || binding.ConnectionID() != spec.ID() || binding.Revision() != spec.Revision() {
		_ = invalidate(f.gate, capability)
		return nil, ErrLifecycleMismatch
	}
	baseChild, startErr := callStart(f.base, ctx, spec)
	if ctx.Err() != nil {
		_ = invalidate(f.gate, capability)
		if baseChild == nil {
			return nil, ErrCancelled
		}
		return newChild(f.gate, capability, baseChild), ErrCancelled
	}
	if startErr != nil || baseChild == nil {
		_ = invalidate(f.gate, capability)
		if baseChild == nil {
			if errors.Is(startErr, supervisor.ErrAuthFailed) {
				return nil, supervisor.ErrAuthFailed
			}
			return nil, ErrRuntimeStart
		}
		if errors.Is(startErr, supervisor.ErrAuthFailed) {
			return newChild(f.gate, capability, baseChild), supervisor.ErrAuthFailed
		}
		return newChild(f.gate, capability, baseChild), ErrRuntimeStart
	}
	return newChild(f.gate, capability, baseChild), nil
}

type child struct {
	gate       *admission.Gate
	capability *admission.LifecycleCapability
	base       supervisor.Child

	mu       sync.Mutex
	stopped  bool
	inFlight bool
	done     chan struct{}
	lastErr  error
}

func newChild(gate *admission.Gate, capability *admission.LifecycleCapability, base supervisor.Child) *child {
	return &child{gate: gate, capability: capability, base: base}
}

// MarkReady atomically activates only this child's exact lifecycle epoch.
// The supervisor calls it with a finite context while still in StatePolling.
func (c *child) MarkReady(ctx context.Context) error {
	if c == nil || c.gate == nil || c.capability == nil || c.base == nil || ctx == nil {
		return ErrInvalidConfig
	}
	if ctx.Err() != nil {
		_ = invalidate(c.gate, c.capability)
		return ErrCancelled
	}
	if err := c.gate.MarkReady(c.capability); err != nil {
		return ErrLifecycleActivation
	}
	// Cancellation racing activation is fail closed. Exact invalidation cannot
	// affect a newer generation that has already replaced this capability.
	if ctx.Err() != nil {
		_ = invalidate(c.gate, c.capability)
		return ErrCancelled
	}
	return nil
}

// Stop invalidates this exact lifecycle before stopping the underlying child.
// Stale invalidation is benign: it proves a newer generation already owns the
// connection and must not be disturbed. Successful Stop is idempotent; a
// failed or panicking Stop remains retryable.
func (c *child) Stop(ctx context.Context) error {
	if c == nil || c.gate == nil || c.capability == nil || c.base == nil || ctx == nil {
		return ErrInvalidConfig
	}
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return nil
	}
	if c.inFlight {
		done := c.done
		c.mu.Unlock()
		select {
		case <-done:
			c.mu.Lock()
			err := c.lastErr
			c.mu.Unlock()
			return err
		case <-ctx.Done():
			return ErrCancelled
		}
	}
	c.inFlight = true
	c.done = make(chan struct{})
	done := c.done
	c.mu.Unlock()

	invalidErr := invalidate(c.gate, c.capability)
	stopErr := callStop(c.base, ctx)
	result := error(nil)
	if invalidErr != nil && !errors.Is(invalidErr, admission.ErrLifecycleMismatch) && !errors.Is(invalidErr, admission.ErrGateClosed) {
		result = ErrLifecycleActivation
	}
	if stopErr != nil {
		if errors.Is(stopErr, context.Canceled) || errors.Is(stopErr, context.DeadlineExceeded) || ctx.Err() != nil {
			result = ErrCancelled
		} else {
			result = ErrRuntimeStop
		}
	}

	c.mu.Lock()
	c.lastErr = result
	if result == nil {
		c.stopped = true
	}
	c.inFlight = false
	close(done)
	c.mu.Unlock()
	return result
}

func invalidate(gate *admission.Gate, capability *admission.LifecycleCapability) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrLifecycleActivation
		}
	}()
	return gate.InvalidateCapability(capability)
}

func callStart(base supervisor.RuntimeFactory, ctx context.Context, spec supervisor.ConnectionSpec) (child supervisor.Child, err error) {
	defer func() {
		if recover() != nil {
			child, err = nil, ErrRuntimeStart
		}
	}()
	return base.Start(ctx, spec)
}

func callStop(base supervisor.Child, ctx context.Context) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrRuntimeStop
		}
	}()
	return base.Stop(ctx)
}

var (
	_ supervisor.RuntimeFactory = (*Factory)(nil)
	_ supervisor.ReadyChild     = (*child)(nil)
)
