//go:build darwin

package proc

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestDarwinSeamLimitIs104(t *testing.T) {
	if got := MaxSocketPathBytes(); got != 104 {
		t.Errorf("MaxSocketPathBytes() = %d, want 104 on darwin", got)
	}
}

// connectedPair returns the two ends of a real unix socket connection.
func connectedPair(t *testing.T) (client, server net.Conn) {
	t.Helper()
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
	client, err = net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	server = <-accepted
	t.Cleanup(func() { client.Close(); server.Close() })
	return client, server
}

func TestDarwinPeerUIDIsTheConnectingProcessUID(t *testing.T) {
	_, server := connectedPair(t)
	uid, err := PeerUID(server)
	if err != nil {
		t.Fatal(err)
	}
	if uid != uint32(os.Getuid()) {
		t.Errorf("PeerUID = %d, want %d", uid, os.Getuid())
	}
}

func TestDarwinPeerUIDRefusesANonUnixConnection(t *testing.T) {
	a, b := net.Pipe()
	defer func() { _ = a.Close() }()
	defer func() { _ = b.Close() }()
	if _, err := PeerUID(a); err == nil {
		t.Error("PeerUID must fail on a connection that is not a unix socket")
	}
}
