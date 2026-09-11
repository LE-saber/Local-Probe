//go:build windows

package probe

import (
	"errors"
	"os"
	"sort"
	"strings"
	"sync"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// windowsProcess owns every handle created for one probe. The process is
// created suspended, assigned to the job, checked, and only then resumed.
// Keeping the process and its primary thread handles here avoids the old
// thread-snapshot race (which could resume an unrelated thread).
type windowsProcess struct {
	mu       sync.Mutex
	job      windows.Handle
	process  windows.Handle
	thread   windows.Handle
	assigned bool
	closed   bool
}

func startFixedProcess(path string, args []string, cwd string, env []string, expected fileIdentity) (*managedProcess, error) {
	if path == "" || cwd == "" {
		return nil, ErrInvalidInput
	}
	guard, err := openWindowsExecutionGuard(path, expected)
	if err != nil {
		return nil, err
	}
	defer guard.close()
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, ErrUnavailable
	}
	state := &windowsProcess{job: job, process: windows.InvalidHandle, thread: windows.InvalidHandle}
	if err := configureKillOnClose(job); err != nil {
		state.close()
		return nil, ErrUnavailable
	}

	stdinRead, stdinWrite, stdoutRead, stdoutWrite, stderrRead, stderrWrite, err := makeProbePipes()
	if err != nil {
		state.close()
		return nil, ErrUnavailable
	}
	// The parent closes the stdin writer immediately after CreateProcess, so
	// the child observes EOF. The three handles in childHandles are the only
	// inherited handles, even if another package accidentally creates an
	// inheritable handle in the parent.
	childHandles := []windows.Handle{stdinRead, stdoutWrite, stderrWrite}
	closeParent := func() {
		for _, handle := range []windows.Handle{stdinRead, stdinWrite, stdoutRead, stdoutWrite, stderrRead, stderrWrite} {
			if handle != windows.InvalidHandle {
				_ = windows.CloseHandle(handle)
			}
		}
	}
	defer closeParent()

	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer attrs.Delete()
	if err := attrs.Update(
		windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST,
		unsafe.Pointer(&childHandles[0]),
		uintptr(len(childHandles))*unsafe.Sizeof(childHandles[0]),
	); err != nil {
		return nil, ErrUnavailable
	}
	appName, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, ErrInvalidInput
	}
	commandLine, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(append([]string{path}, args...)))
	if err != nil {
		return nil, ErrInvalidInput
	}
	cwdPtr, err := windows.UTF16PtrFromString(cwd)
	if err != nil {
		return nil, ErrInvalidInput
	}
	envBlock, err := probeEnvironmentBlock(env)
	if err != nil {
		return nil, ErrInvalidInput
	}

	startup := &windows.StartupInfoEx{
		StartupInfo: windows.StartupInfo{
			Cb:         uint32(unsafe.Sizeof(windows.StartupInfoEx{})),
			Flags:      windows.STARTF_USESTDHANDLES | windows.STARTF_USESHOWWINDOW,
			ShowWindow: windows.SW_HIDE,
			StdInput:   stdinRead,
			StdOutput:  stdoutWrite,
			StdErr:     stderrWrite,
		},
		ProcThreadAttributeList: attrs.List(),
	}
	info := &windows.ProcessInformation{}
	var flags uint32 = windows.CREATE_SUSPENDED | windows.CREATE_NO_WINDOW |
		windows.CREATE_UNICODE_ENVIRONMENT | windows.EXTENDED_STARTUPINFO_PRESENT
	if err := windows.CreateProcess(
		appName,
		commandLine,
		nil,
		nil,
		true,
		flags,
		&envBlock[0],
		cwdPtr,
		&startup.StartupInfo,
		info,
	); err != nil {
		return nil, processStartFailure(err)
	}
	state.process = info.Process
	state.thread = info.Thread

	if err := windows.AssignProcessToJobObject(job, state.process); err != nil {
		state.terminateSetup()
		return nil, ErrUnavailable
	}
	state.assigned = true
	// Child handles are no longer needed by the parent. Keep only the two
	// parent-side output readers alive until ownership moves to os.File.
	for _, handle := range []windows.Handle{stdinRead, stdinWrite, stdoutWrite, stderrWrite} {
		_ = windows.CloseHandle(handle)
	}
	stdinRead, stdinWrite, stdoutWrite, stderrWrite = windows.InvalidHandle, windows.InvalidHandle, windows.InvalidHandle, windows.InvalidHandle

	// The suspended process has not executed user code. Verify the image that
	// Windows actually created before allowing its primary thread to run.
	if err := verifySuspendedImage(state.process, path, expected, guard.identity); err != nil {
		state.terminateSetup()
		return nil, err
	}
	resumed, err := windows.ResumeThread(state.thread)
	if err != nil || resumed != 1 {
		state.terminateSetup()
		return nil, ErrUnavailable
	}
	_ = windows.CloseHandle(state.thread)
	state.thread = windows.InvalidHandle

	stdout := os.NewFile(uintptr(stdoutRead), "local-probe-stdout")
	stderr := os.NewFile(uintptr(stderrRead), "local-probe-stderr")
	if stdout == nil || stderr == nil {
		if stdout != nil {
			_ = stdout.Close()
		}
		if stderr != nil {
			_ = stderr.Close()
		}
		_ = state.kill()
		state.close()
		return nil, ErrUnavailable
	}
	// Ownership of the parent read handles moved to os.File. The deferred
	// cleanup sees the invalidated locals and does not close them a second time.
	stdoutRead, stderrRead = windows.InvalidHandle, windows.InvalidHandle

	return &managedProcess{
		stdout: stdout,
		stderr: stderr,
		wait:   state.wait,
		kill:   state.kill,
		close:  state.close,
	}, nil
}

func makeProbePipes() (stdinRead, stdinWrite, stdoutRead, stdoutWrite, stderrRead, stderrWrite windows.Handle, err error) {
	sa := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), InheritHandle: 1}
	if err := windows.CreatePipe(&stdinRead, &stdinWrite, sa, 0); err != nil {
		return 0, 0, 0, 0, 0, 0, err
	}
	if err := windows.CreatePipe(&stdoutRead, &stdoutWrite, sa, 0); err != nil {
		_ = windows.CloseHandle(stdinRead)
		_ = windows.CloseHandle(stdinWrite)
		return 0, 0, 0, 0, 0, 0, err
	}
	if err := windows.CreatePipe(&stderrRead, &stderrWrite, sa, 0); err != nil {
		_ = windows.CloseHandle(stdinRead)
		_ = windows.CloseHandle(stdinWrite)
		_ = windows.CloseHandle(stdoutRead)
		_ = windows.CloseHandle(stdoutWrite)
		return 0, 0, 0, 0, 0, 0, err
	}
	// Only child-side handles remain inheritable. The attribute handle list is
	// an additional defense, not a substitute for correct handle flags.
	for _, handle := range []windows.Handle{stdinWrite, stdoutRead, stderrRead} {
		if err := windows.SetHandleInformation(handle, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
			for _, closeHandle := range []windows.Handle{stdinRead, stdinWrite, stdoutRead, stdoutWrite, stderrRead, stderrWrite} {
				_ = windows.CloseHandle(closeHandle)
			}
			return 0, 0, 0, 0, 0, 0, err
		}
	}
	return stdinRead, stdinWrite, stdoutRead, stdoutWrite, stderrRead, stderrWrite, nil
}

func configureKillOnClose(job windows.Handle) error {
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS
	info.BasicLimitInformation.ActiveProcessLimit = 1
	_, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	return err
}

func (state *windowsProcess) wait() error {
	state.mu.Lock()
	process := state.process
	closed := state.closed
	state.mu.Unlock()
	if closed || process == windows.InvalidHandle {
		return ErrUnavailable
	}
	result, err := windows.WaitForSingleObject(process, windows.INFINITE)
	if err != nil || result != windows.WAIT_OBJECT_0 {
		return ErrUnavailable
	}
	var exitCode uint32
	if err := windows.GetExitCodeProcess(process, &exitCode); err != nil || exitCode != 0 {
		return ErrUnavailable
	}
	return nil
}

func (state *windowsProcess) kill() error {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed || state.job == windows.InvalidHandle {
		return nil
	}
	if err := windows.TerminateJobObject(state.job, 1); err != nil {
		// The job is configured with KILL_ON_JOB_CLOSE. Closing it is the
		// fail-closed fallback if an explicit termination call is unavailable.
		closeErr := windows.CloseHandle(state.job)
		state.job = windows.InvalidHandle
		if closeErr != nil {
			return err
		}
		return err
	}
	return nil
}

func (state *windowsProcess) terminateSetup() {
	state.mu.Lock()
	if state.closed {
		state.mu.Unlock()
		return
	}
	if state.assigned && state.job != windows.InvalidHandle {
		if err := windows.TerminateJobObject(state.job, 1); err != nil && state.process != windows.InvalidHandle {
			_ = windows.TerminateProcess(state.process, 1)
		}
	} else if state.process != windows.InvalidHandle {
		// Assignment failed while the process is still suspended. It is not
		// covered by the job, so terminate this unassigned setup process before
		// waiting for it.
		_ = windows.TerminateProcess(state.process, 1)
	}
	process := state.process
	thread := state.thread
	state.mu.Unlock()
	if process != windows.InvalidHandle {
		_, _ = windows.WaitForSingleObject(process, windows.INFINITE)
	}
	if thread != windows.InvalidHandle {
		_ = windows.CloseHandle(thread)
	}
	state.close()
}

func (state *windowsProcess) close() {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		return
	}
	if state.thread != windows.InvalidHandle {
		_ = windows.CloseHandle(state.thread)
		state.thread = windows.InvalidHandle
	}
	if state.process != windows.InvalidHandle {
		_ = windows.CloseHandle(state.process)
		state.process = windows.InvalidHandle
	}
	if state.job != windows.InvalidHandle {
		_ = windows.CloseHandle(state.job)
		state.job = windows.InvalidHandle
	}
	state.closed = true
}

func verifySuspendedImage(process windows.Handle, auditedPath string, expected, guarded fileIdentity) error {
	actualPath, err := queryProcessImagePath(process)
	if err != nil {
		return ErrIdentityChanged
	}
	if !sameWindowsPath(actualPath, auditedPath) {
		return ErrIdentityChanged
	}
	// The no-share guard remains held by the caller. Re-opening the pathname
	// is not used as evidence; the guarded handle identity is the expected
	// descriptor identity and blocks rename/delete/write opens during launch.
	if !sameIdentity(expected, guarded) {
		return ErrIdentityChanged
	}
	return nil
}

func queryProcessImagePath(process windows.Handle) (string, error) {
	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(process, 0, &buffer[0], &size); err != nil {
		return "", err
	}
	if size == 0 || int(size) > len(buffer) {
		return "", errors.New("invalid process image path")
	}
	return windows.UTF16ToString(buffer[:size]), nil
}

func sameWindowsPath(a, b string) bool {
	return strings.EqualFold(strings.TrimRight(a, `\\`), strings.TrimRight(b, `\\`))
}

func probeEnvironmentBlock(values []string) ([]uint16, error) {
	copyValues := append([]string(nil), values...)
	for _, value := range copyValues {
		if value == "" || strings.IndexByte(value, 0) >= 0 || !strings.Contains(value, "=") {
			return nil, ErrInvalidInput
		}
	}
	sort.SliceStable(copyValues, func(i, j int) bool {
		return strings.ToUpper(copyValues[i]) < strings.ToUpper(copyValues[j])
	})
	var encoded []uint16
	for _, value := range copyValues {
		encoded = append(encoded, utf16.Encode([]rune(value))...)
		encoded = append(encoded, 0)
	}
	encoded = append(encoded, 0)
	return encoded, nil
}
