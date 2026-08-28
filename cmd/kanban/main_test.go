package main

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestServerURL(t *testing.T) {
	tests := map[string]string{
		"127.0.0.1:3100": "http://127.0.0.1:3100",
		"localhost:4100": "http://localhost:4100",
		"0.0.0.0:3100":   "http://127.0.0.1:3100",
		"[::]:3100":      "http://127.0.0.1:3100",
	}
	for address, wanted := range tests {
		if got := serverURL(address); got != wanted {
			t.Errorf("serverURL(%q) = %q, want %q", address, got, wanted)
		}
	}
}

func TestHasArgument(t *testing.T) {
	if !hasArgument([]string{"--background"}, "--background") {
		t.Fatal("background argument was not found")
	}
	if hasArgument([]string{"--other"}, "--background") {
		t.Fatal("background argument was found unexpectedly")
	}
}

func TestListenLocalSocketIsPrivateAndReachable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix sockets are enabled only on non-Windows hosts")
	}
	path := filepath.Join(t.TempDir(), "kanban.sock")
	listener, err := listenLocalSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("socket permissions = %o, want 600", info.Mode().Perm())
	}
	connection, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	connection.Close()
}

func TestListenLocalSocketRejectsRegularFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix sockets are enabled only on non-Windows hosts")
	}
	path := filepath.Join(t.TempDir(), "kanban.sock")
	if err := os.WriteFile(path, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := listenLocalSocket(path); err == nil {
		t.Fatal("regular file was replaced by local socket")
	}
	contents, err := os.ReadFile(path)
	if err != nil || string(contents) != "keep me" {
		t.Fatalf("regular file changed: contents %q, err %v", contents, err)
	}
}
