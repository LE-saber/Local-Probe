//go:build !windows

package rootfs

import "github.com/LE-saber/Local-Probe/internal/policy"

// Non-Windows targets remain read-only until handle-bound write/exec support
// has its own real-platform tests. Linux Agents and Relays remain usable.
func (s *Source) WithDirectory(policy.BoundScope, string, string, func(string) error) error {
	return ErrUnsupportedType
}
func (s *Source) FileVersion(policy.BoundScope, string, string) (string, error) {
	return "", ErrUnsupportedType
}
func (s *Source) FilePreview(policy.BoundScope, string, string) (string, []byte, error) {
	return "", nil, ErrUnsupportedType
}
func (s *Source) WriteFile(policy.BoundScope, string, string, string, []byte, func([]byte) error) error {
	return ErrUnsupportedType
}
