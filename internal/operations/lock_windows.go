package operations

import (
	"golang.org/x/sys/windows"
	"os"
)

type fsLock struct {
	file    *os.File
	overlap windows.Overlapped
}
