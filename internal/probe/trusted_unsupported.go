//go:build !windows

package probe

import "context"

func startTrustedProcess(context.Context, string, []string, string, []string, fileIdentity, string) (*managedProcess, error) {
	return nil, ErrUnsupported
}
