package main

import "testing"

func TestEnvDefaults(t *testing.T) {
	t.Setenv("HOST", "")
	if got := env("HOST", "127.0.0.1"); got != "127.0.0.1" {
		t.Fatalf("unsafe default bind host: %q", got)
	}
	t.Setenv("HOST", "0.0.0.0")
	if got := env("HOST", "127.0.0.1"); got != "0.0.0.0" {
		t.Fatalf("explicit deployment bind host ignored: %q", got)
	}
}
