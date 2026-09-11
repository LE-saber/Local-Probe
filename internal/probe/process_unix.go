//go:build aix || android || darwin || dragonfly || freebsd || hurd || illumos || linux || netbsd || openbsd || solaris

package probe

// The fixed process launcher is intentionally not available outside Windows
// until the fd-based exec hard gate is implemented. Do not silently fall back
// to an audit-by-path Unix launch.
func startFixedProcess(string, []string, string, []string, fileIdentity) (*managedProcess, error) {
	return nil, ErrUnsupported
}
