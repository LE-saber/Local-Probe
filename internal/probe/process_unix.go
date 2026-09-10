//go:build aix || android || darwin || dragonfly || freebsd || hurd || illumos || linux || netbsd || openbsd || solaris

package probe

import (
	"os/exec"
	"syscall"
)

func startFixedProcess(path string, args []string, cwd string, env []string) (*managedProcess, error) {
	cmd := exec.Command(path, args...)
	cmd.Dir = cwd
	cmd.Env = append([]string(nil), env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, processStartFailure(err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdout.Close()
		return nil, processStartFailure(err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, processStartFailure(err)
	}

	return &managedProcess{
		stdout: stdout,
		stderr: stderr,
		wait:   cmd.Wait,
		kill: func() error {
			// The child is always placed in its own process group.  Killing the
			// group is required so a helper cannot outlive a timed-out probe.
			err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			if err == syscall.ESRCH {
				return nil
			}
			return err
		},
		close: emptyProcessClose,
	}, nil
}
