package main

import "testing"

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
