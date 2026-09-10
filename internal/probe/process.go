package probe

import (
	"os"
)

type managedProcess struct {
	stdout ioReadCloser
	stderr ioReadCloser
	wait   func() error
	kill   func() error
	close  func()
}

// ioReadCloser is the narrow interface needed by the bounded reader.  The
// alias keeps platform process files from growing unrelated dependencies.
type ioReadCloser interface {
	Read([]byte) (int, error)
	Close() error
}

func emptyProcessClose() {}

func processStartFailure(err error) error {
	if err == nil {
		return ErrUnavailable
	}
	if os.IsNotExist(err) {
		return ErrNotFound
	}
	return ErrUnavailable
}
