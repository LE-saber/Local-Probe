//go:build windows

package deviceidentity

import (
	"bytes"
	"io/fs"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var protectionHeader = []byte("LPID1-DPAPI\x00")

func protectDirectory(path string) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}
func checkDirectory(fs.FileInfo) error { return nil }
func checkRecord(fs.FileInfo) error    { return nil }

func seal(data []byte) ([]byte, error) {
	var out windows.DataBlob
	in := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	err := windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	runtime.KeepAlive(data)
	if err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	if out.Size > 2*MaxArtifactBytes {
		return nil, ErrStorage
	}
	return append(append([]byte(nil), protectionHeader...), unsafe.Slice(out.Data, int(out.Size))...), nil
}
func unseal(data []byte) ([]byte, error) {
	if !bytes.HasPrefix(data, protectionHeader) || len(data) == len(protectionHeader) {
		return nil, ErrStorage
	}
	data = data[len(protectionHeader):]
	in := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var out windows.DataBlob
	err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	runtime.KeepAlive(data)
	if err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	if out.Size > MaxArtifactBytes {
		return nil, ErrStorage
	}
	return append([]byte(nil), unsafe.Slice(out.Data, int(out.Size))...), nil
}
