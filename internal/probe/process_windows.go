//go:build windows

package probe

import (
	"errors"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsJobState struct {
	mu     sync.Mutex
	job    windows.Handle
	closed bool
}

func startFixedProcess(path string, args []string, cwd string, env []string) (*managedProcess, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, ErrUnavailable
	}
	state := &windowsJobState{job: job}
	if err := configureKillOnClose(job); err != nil {
		_ = windows.CloseHandle(job)
		return nil, ErrUnavailable
	}

	cmd := exec.Command(path, args...)
	cmd.Dir = cwd
	cmd.Env = append([]string(nil), env...)
	// The process must not execute even its first instruction until it has
	// joined the private job.  CREATE_NO_WINDOW and HideWindow cover console
	// and GUI-style child windows respectively.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_SUSPENDED | windows.CREATE_NO_WINDOW,
		HideWindow:    true,
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		state.close()
		return nil, processStartFailure(err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdout.Close()
		state.close()
		return nil, processStartFailure(err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdout.Close()
		_ = stderr.Close()
		state.close()
		return nil, processStartFailure(err)
	}

	processHandle, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_INFORMATION|windows.SYNCHRONIZE,
		false,
		uint32(cmd.Process.Pid),
	)
	if err != nil {
		// The child is still suspended and has not joined the job.  This is
		// setup-failure cleanup, not the runtime termination path.
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		state.close()
		return nil, processStartFailure(err)
	}
	assigned := windows.AssignProcessToJobObject(job, processHandle) == nil
	if !assigned {
		terminateUnassigned(processHandle)
		_ = windows.CloseHandle(processHandle)
		_ = cmd.Wait()
		state.close()
		return nil, ErrUnavailable
	}
	_ = windows.CloseHandle(processHandle)
	if err := resumeMainThread(uint32(cmd.Process.Pid)); err != nil {
		_ = state.kill()
		_ = cmd.Wait()
		state.close()
		return nil, ErrUnavailable
	}

	return &managedProcess{
		stdout: stdout,
		stderr: stderr,
		wait:   cmd.Wait,
		kill:   state.kill,
		close:  state.close,
	}, nil
}

func configureKillOnClose(job windows.Handle) error {
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	return err
}

func (state *windowsJobState) kill() error {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed || state.job == windows.InvalidHandle {
		return nil
	}
	if err := windows.TerminateJobObject(state.job, 1); err == nil {
		return nil
	}
	// Closing a configured job is itself a tree-termination operation because
	// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE is set.  This is still job supervision,
	// not a parent-only or taskkill fallback.
	err := windows.CloseHandle(state.job)
	state.closed = true
	state.job = windows.InvalidHandle
	return err
}

func (state *windowsJobState) close() {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed || state.job == windows.InvalidHandle {
		return
	}
	_ = windows.CloseHandle(state.job)
	state.closed = true
	state.job = windows.InvalidHandle
}

func terminateUnassigned(process windows.Handle) {
	if process == windows.InvalidHandle {
		return
	}
	// This path is only setup-failure cleanup, before a managed process is
	// returned.  Normal cancellation and timeout always use the Job Object.
	_ = windows.TerminateProcess(process, 1)
}

func resumeMainThread(pid uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)

	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	err = windows.Thread32First(snapshot, &entry)
	for err == nil {
		if entry.OwnerProcessID == pid {
			thread, openErr := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
			if openErr != nil {
				return openErr
			}
			ret, resumeErr := windows.ResumeThread(thread)
			_ = windows.CloseHandle(thread)
			if resumeErr != nil {
				return resumeErr
			}
			if ret != 1 {
				return errors.New("unexpected thread suspend count")
			}
			return nil
		}
		err = windows.Thread32Next(snapshot, &entry)
	}
	return err
}
