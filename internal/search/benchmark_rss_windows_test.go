//go:build windows

package search_test

import (
	"syscall"
	"unsafe"
)

// processMemoryCounters is the stable prefix of Windows
// PROCESS_MEMORY_COUNTERS. WorkingSetSize is the process RSS equivalent used
// by this evidence harness. Failure is reported as unavailable; the harness
// never treats the value as a correctness or safety signal.
type processMemoryCounters struct {
	cb                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

var (
	getCurrentProcess    = syscall.NewLazyDLL("kernel32.dll").NewProc("GetCurrentProcess")
	getProcessMemoryInfo = syscall.NewLazyDLL("psapi.dll").NewProc("GetProcessMemoryInfo")
)

func currentRSSBytes() (uint64, bool) {
	process, _, _ := getCurrentProcess.Call()
	if process == 0 {
		return 0, false
	}
	counters := processMemoryCounters{cb: uint32(unsafe.Sizeof(processMemoryCounters{}))}
	result, _, _ := getProcessMemoryInfo.Call(
		process,
		uintptr(unsafe.Pointer(&counters)),
		uintptr(counters.cb),
	)
	if result == 0 {
		return 0, false
	}
	return uint64(counters.WorkingSetSize), true
}
