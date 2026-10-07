//go:build windows

package probe

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestCurrentAccountAppContainerNativeStartup(t *testing.T) {
	if os.Getenv("LOCAL_PROBE_APPCONTAINER_TEST") != "1" {
		t.Skip("opt-in per-user temporary AppContainer test")
	}
	windowsDirectory, err := windows.GetWindowsDirectory()
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []struct {
		name, path string
		args       []string
	}{
		{"cmd", filepath.Join(windowsDirectory, "System32", "cmd.exe"), []string{"/d", "/c", "exit", "0"}},
		{"node", `D:\Javascript\JS\nodejs\node.exe`, []string{"-e", "process.exit(0)"}},
	} {
		t.Run(candidate.name, func(t *testing.T) {
			if _, err := os.Stat(candidate.path); err != nil {
				t.Fatalf("explicit installed candidate unavailable: %v", err)
			}
			base := t.TempDir()
			bin, work := filepath.Join(base, "bin"), filepath.Join(base, "work")
			for _, path := range []string{bin, work} {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			staged := filepath.Join(bin, filepath.Base(candidate.path))
			source, err := os.Open(candidate.path)
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			info, err := source.Stat()
			if err != nil || !info.Mode().IsRegular() || info.Size() > 128<<20 {
				t.Fatal("candidate must be a bounded ordinary installed executable")
			}
			destination, err := os.OpenFile(staged, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			_, copyErr := io.CopyN(destination, source, info.Size())
			closeErr := destination.Close()
			if copyErr != nil || closeErr != nil {
				t.Fatal("candidate staging failed")
			}
			// A fixed no-op positive control checks the staged image itself. This
			// is not a fallback for a failed isolated task: no user/project script
			// is accepted and no product execution capability is enabled.
			controlContext, controlCancel := context.WithTimeout(context.Background(), 5*time.Second)
			control := exec.CommandContext(controlContext, staged, candidate.args...)
			control.Dir = work
			control.Env, err = fixedEnvironment(work)
			if err != nil {
				controlCancel()
				t.Fatal(err)
			}
			controlErr := control.Run()
			controlCancel()
			if controlErr != nil {
				t.Fatalf("staged fixed no-op positive control: %v", controlErr)
			}
			t.Log("staged fixed no-op works in ordinary local process; NOT an isolation proof")
			user, err := windows.GetCurrentProcessToken().GetTokenUser()
			if err != nil {
				t.Fatal(err)
			}
			profile := newTestAppContainer(t)
			setTestContainerACL(t, bin, user.User.Sid.String(), profile.sid.String(), false)
			setTestContainerACL(t, work, user.User.Sid.String(), profile.sid.String(), true)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			t.Run("restricted_control", func(t *testing.T) {
				startTestRestrictedProcess(t, ctx, profile.sid, staged, candidate.args, work, user.User.Sid, false)
				t.Log("current-account deprivileged control started; this is NOT a filesystem/network isolation capability")
			})
			t.Run("appcontainer", func(t *testing.T) {
				startTestAppContainer(t, ctx, profile.sid, staged, candidate.args, work, user.User.Sid)
				t.Log("installed native candidate exited successfully inside same-account restricted container")
			})
		})
	}
}

// Candidate compatibility/security experiment ONLY. Explicit opt-in prevents
// normal go test runs from creating per-user application profiles. No Windows
// user account/service/firewall rule is created, and no production capability
// is minted. Failure never falls back to unrestricted execution.
func TestCurrentAccountAppContainerIsolation(t *testing.T) {
	if os.Getenv("LOCAL_PROBE_APPCONTAINER_TEST") != "1" {
		t.Skip("opt-in per-user temporary AppContainer test")
	}
	base := t.TempDir()
	bin, work, outside, peer := filepath.Join(base, "bin"), filepath.Join(base, "work"), filepath.Join(base, "outside"), filepath.Join(base, "peer")
	for _, directory := range []string{bin, work, outside, peer} {
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	executable := filepath.Join(bin, "isolation-helper.exe")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	build := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go.exe"), "build", "-o", executable, "./isolationhelper")
	if data, err := build.CombinedOutput(); err != nil {
		t.Fatalf("fixture build: %v %.1024s", err, data)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	userSID := user.User.Sid.String()
	profile := newTestAppContainer(t)
	peerProfile := newTestAppContainer(t)
	// ACL/mandatory-label changes are confined to test-owned temporary paths.
	setTestContainerACL(t, bin, userSID, profile.sid.String(), false)
	setTestContainerACL(t, work, userSID, profile.sid.String(), true)
	setTestContainerACL(t, outside, userSID, "", false)
	setTestContainerACL(t, peer, userSID, peerProfile.sid.String(), true)
	for path, content := range map[string]string{filepath.Join(work, "input.txt"): "authorized-test-input", filepath.Join(outside, "synthetic-secret.txt"): "synthetic-no-real-secret", filepath.Join(peer, "synthetic-secret.txt"): "synthetic-peer-only-input"} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	commonAppFile := filepath.Join(bin, "common-app-resource.txt")
	if err := os.WriteFile(commonAppFile, []byte("shared-application-fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	// A synthetic resource explicitly readable by ALL_APPLICATION_PACKAGES
	// discriminates ordinary AC from the requested LPAC opt-out semantics.
	setTestContainerACL(t, commonAppFile, userSID, "S-1-15-2-1", false)
	tcp4, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcp4.Close()
	tcp6, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcp6.Close()
	udp4, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udp4.Close()
	// Positive controls ensure connection-refused isn't misreported as denial.
	for _, target := range []struct{ network, address string }{{"tcp4", tcp4.Addr().String()}, {"tcp6", tcp6.Addr().String()}, {"udp4", udp4.LocalAddr().String()}} {
		connection, err := net.DialTimeout(target.network, target.address, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if target.network == "udp4" {
			if _, err := connection.Write([]byte("positive-network-control")); err != nil {
				_ = connection.Close()
				t.Fatal(err)
			}
		}
		_ = connection.Close()
	}
	// Verify actual UDP delivery before launch, then distinguish successful
	// send API completion from packets received through the container boundary.
	if err := udp4.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	packet := make([]byte, 128)
	if n, _, err := udp4.ReadFrom(packet); err != nil || string(packet[:n]) != "positive-network-control" {
		t.Fatal("UDP delivery positive control failed")
	}
	args := []string{work, outside, tcp4.Addr().String(), tcp6.Addr().String(), udp4.LocalAddr().String(), peer}
	startTestAppContainer(t, ctx, profile.sid, executable, args, work, user.User.Sid)
	data, err := os.ReadFile(filepath.Join(work, "isolation-result.json"))
	if err != nil {
		t.Fatalf("fixture returned no result: %v", err)
	}
	var result map[string]bool
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(work, "isolation-network-errors.json")); err == nil {
		t.Logf("actual network outcomes: %s", data)
	}
	if err := udp4.SetReadDeadline(time.Now().Add(300 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	n, _, packetErr := udp4.ReadFrom(packet)
	if packetErr == nil {
		t.Errorf("container UDP packet delivered to host fixture (%d bytes)", n)
	} else if timeout, ok := packetErr.(net.Error); !ok || !timeout.Timeout() {
		t.Fatalf("UDP receive observation failed: %v", packetErr)
	} else {
		t.Log("host fixture received no UDP packet within bounded observation; send API still succeeded, not a strict denial PASS")
	}
	for _, name := range []string{"app_container", "not_elevated", "read_allowed", "write_allowed", "outside_read_denied", "outside_write_denied", "readonly_bin_write_denied", "peer_read_denied", "peer_write_denied", "tcp4_denied", "tcp6_denied", "udp4_denied", "listen_denied", "child_runs", "child_inherits_container", "child_not_elevated", "child_read_allowed", "child_write_allowed", "child_outside_read_denied", "child_outside_write_denied", "child_readonly_bin_write_denied", "child_peer_read_denied", "child_peer_write_denied"} {
		t.Logf("%s=%t", name, result[name])
		if !result[name] {
			t.Errorf("isolation candidate failed %s (not an execution capability)", name)
		}
	}
	commonAppCheck := "common_app_read_allowed"
	if os.Getenv("LOCAL_PROBE_LPAC_TEST") == "1" {
		commonAppCheck = "common_app_read_denied"
	}
	for _, name := range []string{commonAppCheck, "child_" + commonAppCheck} {
		t.Logf("requested container resource semantics %s=%t", name, result[name])
		if !result[name] {
			t.Errorf("requested container restriction not established: %s", name)
		}
	}
	for _, directory := range []string{outside, peer, bin} {
		for _, filename := range []string{"output.txt", "child-output.txt"} {
			if _, err := os.Stat(filepath.Join(directory, filename)); !os.IsNotExist(err) {
				t.Error("read-only or unauthorized fixture was written")
			}
		}
	}
	input, err := os.ReadFile(filepath.Join(outside, "synthetic-secret.txt"))
	if err != nil || string(input) != "synthetic-no-real-secret" {
		t.Error("outside fixture changed")
	}
}

type testAppContainer struct{ sid *windows.SID }

func newTestAppContainer(t *testing.T) testAppContainer {
	t.Helper()
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString("LocalProbe.Test." + hex.EncodeToString(nonce[:]))
	if err != nil {
		t.Fatal(err)
	}
	userenv := windows.NewLazySystemDLL("userenv.dll")
	create := userenv.NewProc("CreateAppContainerProfile")
	deleteProfile := userenv.NewProc("DeleteAppContainerProfile")
	if err := create.Find(); err != nil {
		t.Fatal(err)
	}
	if err := deleteProfile.Find(); err != nil {
		t.Fatal(err)
	}
	var sid *windows.SID
	status, _, _ := create.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(name)), 0, 0, uintptr(unsafe.Pointer(&sid)))
	if uint32(status) != 0 {
		t.Fatalf("CreateAppContainerProfile HRESULT=0x%x", uint32(status))
	}
	var folder string
	// Delete only this newly created random profile, never derive/reuse one.
	t.Cleanup(func() {
		status, _, _ := deleteProfile.Call(uintptr(unsafe.Pointer(name)))
		if uint32(status) != 0 {
			t.Errorf("DeleteAppContainerProfile HRESULT=0x%x", uint32(status))
		} else {
			t.Log("temporary application profile deleted; no account was created")
			if folder != "" {
				if _, err := os.Stat(folder); !os.IsNotExist(err) {
					t.Errorf("temporary application storage removal not verified: %v", err)
				}
			}
		}
		if sid != nil {
			if err := windows.FreeSid(sid); err != nil {
				t.Error(err)
			}
		}
	})
	if sid == nil {
		t.Fatal("created profile has no SID")
	}
	getFolder := userenv.NewProc("GetAppContainerFolderPath")
	if err := getFolder.Find(); err != nil {
		t.Fatal(err)
	}
	sidString, _ := windows.UTF16PtrFromString(sid.String())
	var folderPointer *uint16
	status, _, _ = getFolder.Call(uintptr(unsafe.Pointer(sidString)), uintptr(unsafe.Pointer(&folderPointer)))
	if uint32(status) != 0 || folderPointer == nil {
		t.Fatal("cannot verify newly created application storage")
	}
	folder = windows.UTF16PtrToString(folderPointer)
	windows.NewLazySystemDLL("ole32.dll").NewProc("CoTaskMemFree").Call(uintptr(unsafe.Pointer(folderPointer)))
	if _, err := os.Stat(folder); err != nil {
		t.Fatal("new application storage not present")
	}
	return testAppContainer{sid: sid}
}

func setTestContainerACL(t *testing.T, path, userSID, containerSID string, writable bool) {
	t.Helper()
	sddl := "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;" + userSID + ")"
	if containerSID != "" {
		access := "0x1200a9" // read/execute, no write/delete/ACL ownership rights
		if writable {
			access = "0x1301bf"
		} // file generic read/write/execute + delete
		sddl += "(A;OICI;" + access + ";;;" + containerSID + ")"
	}
	if writable {
		sddl += "S:(ML;OICI;NW;;;LW)"
	}
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	flags := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION)
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, flags, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
	if writable {
		sacl, _, err := descriptor.SACL()
		if err != nil {
			t.Fatal(err)
		}
		if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.LABEL_SECURITY_INFORMATION, nil, nil, nil, sacl); err != nil {
			t.Fatal(err)
		}
	}
	runtime.KeepAlive(descriptor)
}

// This test-only launcher reuses existing image guard and Job teardown. It
// creates no general product execution API. Only explicit test-owned stdio
// pipes are inherited, never ambient handles or the host environment. Result
// files must still be written in the synthetic work root; logs cannot substitute.
func startTestAppContainer(t *testing.T, ctx context.Context, sid *windows.SID, path string, args []string, cwd string, parentSID *windows.SID) {
	t.Helper()
	startTestRestrictedProcess(t, ctx, sid, path, args, cwd, parentSID, true)
}

func startTestRestrictedProcess(t *testing.T, ctx context.Context, sid *windows.SID, path string, args []string, cwd string, parentSID *windows.SID, container bool) {
	t.Helper()
	descriptor, err := AuditExecutable(ToolNode, path)
	if err != nil {
		t.Fatal(err)
	}
	guard, err := openWindowsExecutionGuard(path, descriptor.identity)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.close()
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	state := &windowsProcess{job: job, process: windows.InvalidHandle, thread: windows.InvalidHandle}
	defer func() {
		if state.process != windows.InvalidHandle {
			_ = state.kill()
		}
		state.close()
	}()
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS
	limits.BasicLimitInformation.ActiveProcessLimit = 4
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		t.Fatal(err)
	}
	// LPAC is an opt-in, stricter per-process candidate, not a machine policy
	// change. Never silently retry with regular AppContainer on failure.
	lessPrivileged := container && os.Getenv("LOCAL_PROBE_LPAC_TEST") == "1"
	attributeCount := uint32(2) // container capabilities + explicit stdio handle list
	if lessPrivileged {
		attributeCount++
	}
	attributes, err := windows.NewProcThreadAttributeList(attributeCount)
	if err != nil {
		t.Fatal(err)
	}
	defer attributes.Delete()
	stdinRead, stdinWrite, stdoutRead, stdoutWrite, stderrRead, stderrWrite, err := makeProbePipes()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, handle := range []windows.Handle{stdinRead, stdinWrite, stdoutRead, stdoutWrite, stderrRead, stderrWrite} {
			if handle != windows.InvalidHandle {
				_ = windows.CloseHandle(handle)
			}
		}
	}()
	childHandles := []windows.Handle{stdinRead, stdoutWrite, stderrWrite}
	if err := attributes.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&childHandles[0]), uintptr(len(childHandles))*unsafe.Sizeof(childHandles[0])); err != nil {
		t.Fatal(err)
	}
	capabilities := struct {
		AppContainerSID *windows.SID
		Capabilities    *windows.SIDAndAttributes
		Count           uint32
		Reserved        uint32
	}{AppContainerSID: sid}
	if container {
		if err := attributes.Update(0x20009, unsafe.Pointer(&capabilities), unsafe.Sizeof(capabilities)); err != nil {
			t.Fatal(err)
		}
	}
	allPackagesOptOut := uint32(1) // PROCESS_CREATION_ALL_APPLICATION_PACKAGES_OPT_OUT
	if lessPrivileged {
		if err := attributes.Update(0x2000f, unsafe.Pointer(&allPackagesOptOut), unsafe.Sizeof(allPackagesOptOut)); err != nil {
			t.Fatalf("LPAC per-process attribute: %v", err)
		}
	}
	startup := windows.StartupInfoEx{StartupInfo: windows.StartupInfo{
		Cb: uint32(unsafe.Sizeof(windows.StartupInfoEx{})), Flags: windows.STARTF_USESTDHANDLES,
		StdInput: stdinRead, StdOutput: stdoutWrite, StdErr: stderrWrite,
	}, ProcThreadAttributeList: attributes.List()}
	// A private noninteractive station avoids sharing the user's UI/clipboard,
	// and supplies explicit container rights without editing existing desktops.
	startup.StartupInfo.Desktop, err = windows.UTF16PtrFromString(newTestPrivateDesktop(t, parentSID.String(), sid.String()))
	if err != nil {
		t.Fatal(err)
	}
	app, _ := windows.UTF16PtrFromString(path)
	command, _ := windows.UTF16PtrFromString(windows.ComposeCommandLine(append([]string{path}, args...)))
	work, _ := windows.UTF16PtrFromString(cwd)
	env, err := fixedEnvironment(cwd)
	if err != nil {
		t.Fatal(err)
	}
	// Windows needs LOCALAPPDATA to establish the per-user AppContainer
	// environment (also retained by Chromium's sandbox environment filter).
	// Resolve only this known folder; never inherit the parent's entire env.
	localAppData, err := windows.KnownFolderPath(windows.FOLDERID_LocalAppData, 0)
	if err != nil {
		t.Fatal(err)
	}
	env = append(env, "LOCALAPPDATA="+localAppData)
	windowsDirectory, err := windows.GetWindowsDirectory()
	if err != nil {
		t.Fatal(err)
	}
	env = append(env, "SystemDrive="+filepath.VolumeName(windowsDirectory))
	block, err := probeEnvironmentBlock(env)
	if err != nil {
		t.Fatal(err)
	}
	var process windows.ProcessInformation
	if err := ctx.Err(); err != nil {
		t.Fatal(err)
	}
	workerToken := currentAccountRestrictedToken(t, parentSID)
	defer workerToken.Close()
	flags := uint32(windows.CREATE_SUSPENDED | windows.CREATE_NO_WINDOW | windows.CREATE_UNICODE_ENVIRONMENT | windows.EXTENDED_STARTUPINFO_PRESENT)
	if err := windows.CreateProcessAsUser(workerToken, app, command, nil, nil, true, flags, &block[0], work, &startup.StartupInfo, &process); err != nil {
		t.Fatalf("CreateProcessAsUser AppContainer: %v", err)
	}
	state.process, state.thread = process.Process, process.Thread
	for _, handle := range []windows.Handle{stdinRead, stdinWrite, stdoutWrite, stderrWrite} {
		_ = windows.CloseHandle(handle)
	}
	stdinRead, stdinWrite, stdoutWrite, stderrWrite = windows.InvalidHandle, windows.InvalidHandle, windows.InvalidHandle, windows.InvalidHandle
	stdout := os.NewFile(uintptr(stdoutRead), "test-container-stdout")
	stderr := os.NewFile(uintptr(stderrRead), "test-container-stderr")
	stdoutRead, stderrRead = windows.InvalidHandle, windows.InvalidHandle
	defer stdout.Close()
	defer stderr.Close()
	if err := windows.AssignProcessToJobObject(job, state.process); err != nil {
		state.terminateSetup()
		t.Fatal(err)
	}
	state.assigned = true
	if err := verifySuspendedImage(state.process, path, descriptor.identity, guard.identity); err != nil {
		t.Fatal(err)
	}
	var token windows.Token
	if err := windows.OpenProcessToken(state.process, windows.TOKEN_QUERY, &token); err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil || !user.User.Sid.Equals(parentSID) {
		t.Fatal("worker does not use the current account")
	}
	var isContainer, length uint32
	if err := windows.GetTokenInformation(token, 29, (*byte)(unsafe.Pointer(&isContainer)), 4, &length); err != nil || (isContainer == 1) != container {
		t.Fatal("worker AppContainer state mismatched requested diagnostic mode")
	}
	if container {
		buffer := make([]byte, 256)
		if err := windows.GetTokenInformation(token, 31, &buffer[0], uint32(len(buffer)), &length); err != nil {
			t.Fatal(err)
		}
		actualSID := *(**windows.SID)(unsafe.Pointer(&buffer[0]))
		if actualSID == nil || !actualSID.Equals(sid) {
			t.Fatal("worker container SID mismatch")
		}
		runtime.KeepAlive(buffer)
	}
	if lessPrivileged {
		var isLPAC uint32
		// TokenIsLessPrivilegedAppContainer = 46 (Windows SDK winnt.h).
		err := windows.GetTokenInformation(token, 46, (*byte)(unsafe.Pointer(&isLPAC)), 4, &length)
		if err == windows.ERROR_INVALID_PARAMETER {
			// No payload is valid on failure. This is only a known fixed test
			// fixture, not an admitted task: verify the opt-out's resource
			// semantics separately rather than inventing an LPAC attestation.
			t.Log("LPAC attribute requested; class 46 query unavailable; no production LPAC proof issued")
		} else if err != nil || length != 4 || isLPAC == 0 {
			t.Fatalf("worker LPAC query failed: flag=%d err=%v", isLPAC, err)
		} else {
			t.Log("verified stricter LPAC token before resume")
		}
	}
	if testTokenElevated(t, token) {
		t.Fatal("worker retained elevation")
	}
	if err := ctx.Err(); err != nil {
		t.Fatal(err)
	}
	// Bound diagnostics independently of task result files. Unknown output,
	// failure to write the results, or failed network assertions still FAIL.
	output := make(chan string, 2)
	for _, stream := range []*os.File{stdout, stderr} {
		go func(stream *os.File) {
			data, _ := io.ReadAll(io.LimitReader(stream, 16<<10))
			output <- string(data)
		}(stream)
	}
	defer func() {
		_ = state.kill()
		for range 2 {
			select {
			case data := <-output:
				if data != "" {
					t.Logf("bounded worker diagnostic: %s", data)
				}
			case <-time.After(time.Second):
				_ = stdout.Close()
				_ = stderr.Close()
				t.Error("worker diagnostic pipe did not close within budget")
			}
		}
	}()
	count, err := windows.ResumeThread(state.thread)
	if err != nil || count != 1 {
		t.Fatal("cannot resume verified worker")
	}
	deadline := time.Now().Add(8 * time.Second)
	for {
		status, err := windows.WaitForSingleObject(state.process, 25)
		if err != nil {
			t.Fatal(err)
		}
		if status == windows.WAIT_OBJECT_0 {
			break
		}
		if status != uint32(windows.WAIT_TIMEOUT) {
			t.Fatal("unexpected worker wait state")
		}
		if ctx.Err() != nil || time.Now().After(deadline) {
			t.Fatal("bounded AppContainer compatibility test timed out")
		}
	}
	var exit uint32
	if err := windows.GetExitCodeProcess(state.process, &exit); err != nil {
		t.Fatal(err)
	}
	if exit != 0 {
		t.Fatal(fmt.Sprintf("isolated worker exit=0x%x", exit))
	}
	runtime.KeepAlive(capabilities)
	runtime.KeepAlive(allPackagesOptOut)
}

func currentAccountRestrictedToken(t *testing.T, parentSID *windows.SID) windows.Token {
	t.Helper()
	var original windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE|windows.TOKEN_ASSIGN_PRIMARY|windows.TOKEN_ADJUST_DEFAULT, &original); err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	base := original
	if testTokenElevated(t, original) {
		linked, err := original.GetLinkedToken()
		if err != nil {
			t.Log("no UAC linked token; explicitly restrict current primary token and verify before launch")
		} else {
			defer linked.Close()
			user, err := linked.GetTokenUser()
			if err != nil || !user.User.Sid.Equals(parentSID) || testTokenElevated(t, linked) {
				t.Fatal("linked token is not the same unelevated account")
			}
			base = linked
		}
	}
	create := windows.NewLazySystemDLL("advapi32.dll").NewProc("CreateRestrictedToken")
	if err := create.Find(); err != nil {
		t.Fatal(err)
	}
	var restricted windows.Token
	// DISABLE_MAX_PRIVILEGE | LUA_TOKEN. Never SANDBOX_INERT (AppLocker bypass).
	ok, _, callErr := create.Call(uintptr(base), 0x1|0x4, 0, 0, 0, 0, 0, 0, uintptr(unsafe.Pointer(&restricted)))
	if ok == 0 {
		t.Fatalf("CreateRestrictedToken: %v", callErr)
	}
	if testTokenElevated(t, restricted) {
		_ = restricted.Close()
		t.Fatal("restricted worker token retained elevation")
	}
	// The elevated caller may have a default DACL granting only Administrators
	// and SYSTEM full access, with read/execute for its logon SID. LUA marks
	// Administrators deny-only. Objects newly created by the worker must remain
	// accessible to its enabled user SID (including its own process/thread).
	// Change ONLY this newly created token, never the parent's or host objects.
	security, err := windows.SecurityDescriptorFromString("D:(A;;GA;;;SY)(A;;GA;;;" + parentSID.String() + ")")
	if err != nil {
		_ = restricted.Close()
		t.Fatal(err)
	}
	dacl, _, err := security.DACL()
	if err != nil || dacl == nil {
		_ = restricted.Close()
		t.Fatal("cannot construct private worker default DACL")
	}
	defaultDACL := struct{ ACL *windows.ACL }{dacl}
	if err := windows.SetTokenInformation(restricted, windows.TokenDefaultDacl, (*byte)(unsafe.Pointer(&defaultDACL)), uint32(unsafe.Sizeof(defaultDACL))); err != nil {
		_ = restricted.Close()
		t.Fatalf("set private worker default DACL: %v", err)
	}
	runtime.KeepAlive(security)
	return restricted
}

// This non-executing regression test needs no application profile. It verifies
// the exact new-token DACL and that configuring it never changes the caller.
func TestCurrentAccountRestrictedDefaultDACL(t *testing.T) {
	parent := windows.GetCurrentProcessToken()
	user, err := parent.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	before := testTokenDefaultDACL(t, parent)
	elevatedBefore := testTokenElevated(t, parent)
	worker := currentAccountRestrictedToken(t, user.User.Sid)
	defer worker.Close()
	expected, err := windows.SecurityDescriptorFromString("D:(A;;GA;;;SY)(A;;GA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		t.Fatal(err)
	}
	if testTokenDefaultDACL(t, worker) != expected.String() {
		t.Fatal("worker default DACL must grant only SYSTEM and its enabled user SID")
	}
	if testTokenDefaultDACL(t, parent) != before || testTokenElevated(t, parent) != elevatedBefore {
		t.Fatal("private worker configuration changed the parent token")
	}
	if testTokenElevated(t, worker) {
		t.Fatal("default DACL fix restored elevation")
	}
	groups, err := worker.GetTokenGroups()
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range groups.AllGroups() {
		if group.Sid.IsWellKnown(windows.WinBuiltinAdministratorsSid) &&
			(group.Attributes&windows.SE_GROUP_ENABLED != 0 || group.Attributes&windows.SE_GROUP_USE_FOR_DENY_ONLY == 0) {
			t.Fatal("worker retained an enabled Administrators group")
		}
	}
	privilegeData := testTokenInformation(t, worker, windows.TokenPrivileges)
	privileges := (*windows.Tokenprivileges)(unsafe.Pointer(&privilegeData[0]))
	name, _ := windows.UTF16PtrFromString("SeChangeNotifyPrivilege")
	var allowed windows.LUID
	if err := windows.LookupPrivilegeValue(nil, name, &allowed); err != nil {
		t.Fatal(err)
	}
	for _, privilege := range privileges.AllPrivileges() {
		if privilege.Luid != allowed {
			t.Fatal("worker retained a privilege outside DISABLE_MAX_PRIVILEGE exception")
		}
	}
	runtime.KeepAlive(privilegeData)
}

func testTokenDefaultDACL(t *testing.T, token windows.Token) string {
	t.Helper()
	data := testTokenInformation(t, token, windows.TokenDefaultDacl)
	if len(data) < int(unsafe.Sizeof(uintptr(0))) {
		t.Fatal("short default DACL information")
	}
	dacl := *(**windows.ACL)(unsafe.Pointer(&data[0]))
	if dacl == nil {
		t.Fatal("default DACL must not allow everyone through a NULL ACL")
	}
	sd, err := windows.NewSecurityDescriptor()
	if err != nil {
		t.Fatal(err)
	}
	if err := sd.SetDACL(dacl, true, false); err != nil {
		t.Fatal(err)
	}
	value := sd.String()
	if value == "" {
		t.Fatal("cannot inspect token default DACL")
	}
	runtime.KeepAlive(data)
	return value
}

func testTokenInformation(t *testing.T, token windows.Token, class uint32) []byte {
	t.Helper()
	var size uint32
	err := windows.GetTokenInformation(token, class, nil, 0, &size)
	if err != windows.ERROR_INSUFFICIENT_BUFFER || size < 4 || size > 64<<10 {
		t.Fatalf("token information query sizing: class=%d err=%v", class, err)
	}
	data := make([]byte, size)
	if err := windows.GetTokenInformation(token, class, &data[0], size, &size); err != nil {
		t.Fatal(err)
	}
	return data
}

func testTokenElevated(t *testing.T, token windows.Token) bool {
	t.Helper()
	var elevated, length uint32
	if err := windows.GetTokenInformation(token, windows.TokenElevation, (*byte)(unsafe.Pointer(&elevated)), 4, &length); err != nil || length != 4 {
		t.Fatalf("cannot verify token elevation: %v", err)
	}
	return elevated != 0
}

func newTestPrivateDesktop(t *testing.T, userSID, containerSID string) string {
	t.Helper()
	user32 := windows.NewLazySystemDLL("user32.dll")
	createStation, getStation, setStation := user32.NewProc("CreateWindowStationW"), user32.NewProc("GetProcessWindowStation"), user32.NewProc("SetProcessWindowStation")
	createDesktop, getDesktop, setDesktop := user32.NewProc("CreateDesktopW"), user32.NewProc("GetThreadDesktop"), user32.NewProc("SetThreadDesktop")
	closeStation, closeDesktop := user32.NewProc("CloseWindowStation"), user32.NewProc("CloseDesktop")
	for _, procedure := range []*windows.LazyProc{createStation, getStation, setStation, createDesktop, getDesktop, setDesktop, closeStation, closeDesktop} {
		if err := procedure.Find(); err != nil {
			t.Fatal(err)
		}
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	stationName := "LocalProbe-Test-" + hex.EncodeToString(nonce[:])
	name, _ := windows.UTF16PtrFromString(stationName)
	security, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;SY)(A;;GA;;;" + userSID + ")(A;;GA;;;" + containerSID + ")S:(ML;;NW;;;LW)")
	if err != nil {
		t.Fatal(err)
	}
	attributes := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: security}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	originalStation, _, _ := getStation.Call()
	originalDesktop, _, _ := getDesktop.Call(uintptr(windows.GetCurrentThreadId()))
	if originalStation == 0 || originalDesktop == 0 {
		t.Fatal("cannot record test process station/desktop")
	}
	station, _, callErr := createStation.Call(uintptr(unsafe.Pointer(name)), 1, windows.GENERIC_ALL, uintptr(unsafe.Pointer(&attributes)))
	if station == 0 {
		t.Fatalf("CreateWindowStation private test object: %v", callErr)
	}
	var desktop uintptr
	t.Cleanup(func() {
		if desktop != 0 {
			if ok, _, err := closeDesktop.Call(desktop); ok == 0 {
				t.Errorf("CloseDesktop: %v", err)
			}
		}
		if ok, _, err := closeStation.Call(station); ok == 0 {
			t.Errorf("CloseWindowStation: %v", err)
		}
	})
	defer func() {
		if ok, _, err := setDesktop.Call(originalDesktop); ok == 0 {
			t.Errorf("restore test thread desktop: %v", err)
		}
		if ok, _, err := setStation.Call(originalStation); ok == 0 {
			t.Errorf("restore test process station: %v", err)
		}
	}()
	if ok, _, err := setStation.Call(station); ok == 0 {
		t.Fatalf("SetProcessWindowStation private test object: %v", err)
	}
	desktopName, _ := windows.UTF16PtrFromString("default")
	desktop, _, callErr = createDesktop.Call(uintptr(unsafe.Pointer(desktopName)), 0, 0, 0, windows.GENERIC_ALL, uintptr(unsafe.Pointer(&attributes)))
	if desktop == 0 {
		t.Fatalf("CreateDesktop private test object: %v", callErr)
	}
	runtime.KeepAlive(security)
	return stationName + `\default`
}
