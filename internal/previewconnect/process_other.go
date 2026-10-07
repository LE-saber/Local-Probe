//go:build !windows

package previewconnect

import "os/exec"

func prepareCommand(_ *exec.Cmd) {}
