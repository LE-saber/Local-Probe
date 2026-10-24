package main

import (
	"context"
	"net"
	"syscall"
	"testing"
)

func TestListenerMSSAppliesOnlyToItsSocket(t *testing.T) {
	for _, value := range []int{-1, 535, 1461} {
		if _, err := edgeListenConfig(value); err == nil {
			t.Fatal("invalid TCP MSS accepted")
		}
	}
	config, err := edgeListenConfig(1100)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := config.Listen(context.Background(), "tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	raw, err := listener.(*net.TCPListener).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var mss int
	var optionErr error
	err = raw.Control(func(fd uintptr) {
		mss, optionErr = syscall.GetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_MAXSEG)
	})
	if err != nil || optionErr != nil || mss != 1100 {
		t.Fatalf("listener MSS=%d, control error=%v, option error=%v", mss, err, optionErr)
	}
}
