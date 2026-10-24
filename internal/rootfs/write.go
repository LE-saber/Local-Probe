package rootfs

import "errors"

var (
	ErrWriteConflict  = errors.New("file version conflict")
	ErrWriteUncertain = errors.New("file write outcome uncertain; retained private before image")
)
