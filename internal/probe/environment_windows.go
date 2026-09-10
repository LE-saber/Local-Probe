//go:build windows

package probe

import "golang.org/x/sys/windows"

func fixedEnvironment(cwd string) ([]string, error) {
	if cwd == "" {
		return nil, ErrInvalidInput
	}
	windowsDir, err := windows.GetWindowsDirectory()
	if err != nil || windowsDir == "" {
		return nil, ErrUnavailable
	}
	return []string{
		"SystemRoot=" + windowsDir,
		"WINDIR=" + windowsDir,
		"PATH=" + windowsDir + `\System32`,
		"TEMP=" + cwd,
		"TMP=" + cwd,
		"HOME=" + cwd,
		"USERPROFILE=" + cwd,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=NUL",
		"GIT_CONFIG_SYSTEM=NUL",
		"GIT_PAGER=cat",
		"GIT_EXTERNAL_DIFF=",
		"GIT_DIFF_OPTS=",
		"GIT_OPTIONAL_LOCKS=0",
		"PYTHONNOUSERSITE=1",
		"PYTHONUTF8=1",
		"LC_ALL=C",
		"LANG=C",
	}, nil
}
