package probe

import (
	"context"
	"golang.org/x/sys/windows"
	"testing"
	"time"
	"unsafe"
)

func TestTrustedJobLimitsAreInstalledInKernel(t *testing.T) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(job)
	if err = configureProcessJob(job, true); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err = configureTrustedCPUTime(job, ctx); err != nil {
		t.Fatal(err)
	}
	var actual windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	if err = windows.QueryInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&actual)), uint32(unsafe.Sizeof(actual)), nil); err != nil {
		t.Fatal(err)
	}
	flags := uint32(windows.JOB_OBJECT_LIMIT_JOB_MEMORY | windows.JOB_OBJECT_LIMIT_JOB_TIME | windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS)
	if actual.BasicLimitInformation.LimitFlags&flags != flags || actual.JobMemoryLimit != 1<<30 || actual.BasicLimitInformation.ActiveProcessLimit != 32 || actual.BasicLimitInformation.PerJobUserTimeLimit <= 0 || actual.BasicLimitInformation.PerJobUserTimeLimit > 3*10000000 {
		t.Fatal("kernel job limits do not match the trusted-host contract")
	}
}
