package main

import "testing"

func TestIsLoopbackBind(t *testing.T) {
	loopback := []string{"127.0.0.1:8743", "localhost:8743", "[::1]:8743"}
	for _, a := range loopback {
		if !isLoopbackBind(a) {
			t.Errorf("isLoopbackBind(%q) = false, want true", a)
		}
	}
	exposed := []string{":8743", "0.0.0.0:8743", "192.168.1.10:8743", "[::]:8743"}
	for _, a := range exposed {
		if isLoopbackBind(a) {
			t.Errorf("isLoopbackBind(%q) = true, want false", a)
		}
	}
}

func TestGuardBind(t *testing.T) {
	// Loopback: always fine, even without a token.
	if err := guardBind("127.0.0.1:8743", "", false); err != nil {
		t.Errorf("loopback without token should be allowed, got %v", err)
	}
	// Exposed without token and without --insecure: refused.
	if err := guardBind("0.0.0.0:8743", "", false); err == nil {
		t.Error("exposed bind without token should be refused")
	}
	// Exposed with a token: fine.
	if err := guardBind("0.0.0.0:8743", "tok", false); err != nil {
		t.Errorf("exposed bind with token should be allowed, got %v", err)
	}
	// Exposed with --insecure: allowed (explicit override).
	if err := guardBind("0.0.0.0:8743", "", true); err != nil {
		t.Errorf("exposed bind with --insecure should be allowed, got %v", err)
	}
}
