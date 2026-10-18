//go:build linux

package proc

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestLinuxSeamLimitIs108(t *testing.T) {
	if got := MaxSocketPathBytes(); got != 108 {
		t.Errorf("MaxSocketPathBytes() = %d, want 108 on Linux", got)
	}
}

func TestLinuxPeerUIDIsTheConnectingProcessUID(t *testing.T) {
	path := filepath.Join(shortDir(t), "s")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, _ := ln.Accept()
		accepted <- c
	}()
	client, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	server := <-accepted
	defer func() { _ = server.Close() }()

	uid, err := PeerUID(server)
	if err != nil {
		t.Fatal(err)
	}
	if uid != uint32(os.Getuid()) {
		t.Errorf("PeerUID = %d, want %d", uid, os.Getuid())
	}
}

func TestLinuxPeerUIDRefusesANonUnixConnection(t *testing.T) {
	a, b := net.Pipe()
	defer func() { _ = a.Close() }()
	defer func() { _ = b.Close() }()
	if _, err := PeerUID(a); err == nil {
		t.Error("PeerUID must fail on a connection that is not a unix socket")
	}
}
