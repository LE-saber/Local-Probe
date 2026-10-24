package operations

import (
	"io/fs"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func protectJournal(path string) error {
	u, e := windows.GetCurrentProcessToken().GetTokenUser()
	if e != nil {
		return e
	}
	s, e := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;" + u.User.Sid.String() + ")")
	if e != nil {
		return e
	}
	a, _, e := s.DACL()
	if e != nil {
		return e
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, a, nil)
}
func checkPrivateJournal(path string, info fs.FileInfo) error {
	attrs, ok := info.Sys().(*syscall.Win32FileAttributeData)
	if !ok || attrs.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return ErrStorage
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return ErrStorage
	}
	actual, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return ErrStorage
	}
	control, _, err := actual.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return ErrStorage
	}
	acl, _, err := actual.DACL()
	if err != nil || acl == nil || acl.AceCount != 2 {
		return ErrStorage
	}
	system, err := windows.StringToSid("S-1-5-18")
	if err != nil {
		return ErrStorage
	}
	ownerSeen, systemSeen := false, false
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(acl, i, &ace) != nil || ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 || ace.Mask != 0x001f01ff { // FILE_ALL_ACCESS
			return ErrStorage
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		switch {
		case windows.EqualSid(sid, user.User.Sid):
			ownerSeen = true
		case windows.EqualSid(sid, system):
			systemSeen = true
		default:
			return ErrStorage
		}
	}
	if !ownerSeen || !systemSeen {
		return ErrStorage
	}
	return nil
}
func sealJournal(b []byte) ([]byte, error) {
	var out windows.DataBlob
	in := windows.DataBlob{Size: uint32(len(b)), Data: &b[0]}
	e := windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	runtime.KeepAlive(b)
	if e != nil {
		return nil, e
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	if out.Size > 2*maxRecord {
		return nil, ErrStorage
	}
	return append([]byte(nil), unsafe.Slice(out.Data, int(out.Size))...), nil
}
func unsealJournal(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return nil, ErrStorage
	}
	var out windows.DataBlob
	in := windows.DataBlob{Size: uint32(len(b)), Data: &b[0]}
	e := windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	runtime.KeepAlive(b)
	if e != nil {
		return nil, e
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	if out.Size > maxRecord {
		return nil, ErrStorage
	}
	return append([]byte(nil), unsafe.Slice(out.Data, int(out.Size))...), nil
}
func lockEngine(f *fsLock) error {
	f.overlap = windows.Overlapped{}
	return windows.LockFileEx(windows.Handle(f.file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &f.overlap)
}
