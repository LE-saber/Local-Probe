//go:build !windows

package auditreader

import "os"

func fileInfoReparse(info os.FileInfo) bool {
	return false
}

func openAuditFile(path string) (*os.File, error) {
	return os.Open(path)
}
