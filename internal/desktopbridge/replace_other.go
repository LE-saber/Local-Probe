//go:build !windows

package desktopbridge

import "os"

func replaceDesktopFile(source, target string) error { return os.Rename(source, target) }
