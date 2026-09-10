//go:build aix || android || darwin || dragonfly || freebsd || hurd || illumos || linux || netbsd || openbsd || solaris

package probe

func fixedEnvironment(cwd string) ([]string, error) {
	if cwd == "" {
		return nil, ErrInvalidInput
	}
	// Every entry is package-owned.  In particular, no caller environment is
	// copied, and PATH is only a fixed compatibility value for child runtimes;
	// the executable itself is always an audited absolute path.
	return []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=" + cwd,
		"XDG_CONFIG_HOME=" + cwd,
		"XDG_CACHE_HOME=" + cwd,
		"XDG_DATA_HOME=" + cwd,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
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
