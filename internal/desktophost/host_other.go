//go:build !windows

package desktophost

import "errors"

var (
	ErrAlreadyRunning = errors.New("desktop host already running")
	ErrUnsupported    = errors.New("desktop host requires Windows amd64")
	ErrSecureHost     = errors.New("desktop host security initialization failed")
)

type Options struct {
	ConfigPath string
	AuditDir   string
	RepoRoot   string
	Transport  string
}

func Run(Options) error      { return ErrUnsupported }
func ShowStartupError(error) {}
