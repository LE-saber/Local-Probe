//go:build !windows

package workspaceadmin

import (
	"context"
	"path/filepath"
	"sync"
	"time"
)

type mutationLocker interface {
	acquire(context.Context) (func(), error)
}

type processMutationLocker struct {
	gate chan struct{}
}

var processMutationLockers sync.Map // map[string]*processMutationLocker

func newMutationLocker(path string) mutationLocker {
	key := filepath.Clean(path)
	if lock, ok := processMutationLockers.Load(key); ok {
		return lock.(*processMutationLocker)
	}
	lock := &processMutationLocker{gate: make(chan struct{}, 1)}
	lock.gate <- struct{}{}
	actual, _ := processMutationLockers.LoadOrStore(key, lock)
	return actual.(*processMutationLocker)
}

func (l *processMutationLocker) acquire(ctx context.Context) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-l.gate:
			var once sync.Once
			return func() { once.Do(func() { l.gate <- struct{}{} }) }, nil
		default:
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, ctx.Err()
		case <-l.gate:
			if !timer.Stop() {
				<-timer.C
			}
			var once sync.Once
			return func() { once.Do(func() { l.gate <- struct{}{} }) }, nil
		case <-timer.C:
		}
	}
}
