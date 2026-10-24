package main

import (
	"errors"
	"net"
	"syscall"
)

// This option affects only this listener, never host routes or firewall rules.
func edgeListenConfig(mss int) (net.ListenConfig, error) {
	var config net.ListenConfig
	if mss == 0 {
		return config, nil
	}
	if mss < 536 || mss > 1460 {
		return config, errors.New("TCP MSS must be 0 or between 536 and 1460")
	}
	config.Control = func(_, _ string, raw syscall.RawConn) error {
		var optionErr error
		if err := raw.Control(func(fd uintptr) {
			optionErr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_MAXSEG, mss)
		}); err != nil {
			return err
		}
		return optionErr
	}
	return config, nil
}
