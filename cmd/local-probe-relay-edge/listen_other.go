//go:build !linux

package main

import (
	"errors"
	"net"
)

func edgeListenConfig(mss int) (net.ListenConfig, error) {
	if mss != 0 {
		return net.ListenConfig{}, errors.New("explicit TCP MSS is supported only on Linux")
	}
	return net.ListenConfig{}, nil
}
