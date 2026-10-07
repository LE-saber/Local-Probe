//go:build !windows

package runtimeowner

// NewJob is intentionally unavailable outside Windows until an equivalent
// handle-based process/ownership primitive is designed for that platform.
func NewJob() (*Job, error) { return nil, ErrUnsupported }
