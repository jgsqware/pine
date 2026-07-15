package main

import (
	"strings"
	"testing"
)

func TestUnitFile(t *testing.T) {
	got := unitFile("/usr/local/bin/pine", "127.0.0.1:8743", "/home/u/.pine", false, "", "", false)

	want := `ExecStart="/usr/local/bin/pine" serve --addr 127.0.0.1:8743 --data "/home/u/.pine"`
	if !strings.Contains(got, want) {
		t.Errorf("ExecStart missing/incorrect.\nwant line: %s\ngot:\n%s", want, got)
	}
	// %h must survive as a literal for systemd to expand (not be eaten by fmt).
	if !strings.Contains(got, "Environment=HOME=%h") {
		t.Errorf("HOME env not rendered literally:\n%s", got)
	}
	if !strings.Contains(got, "WantedBy=default.target") {
		t.Errorf("missing install target:\n%s", got)
	}
	// A loopback install carries no token env and no --insecure.
	if strings.Contains(got, "PINE_TOKEN") || strings.Contains(got, "--insecure") {
		t.Errorf("loopback unit should not carry a token or --insecure:\n%s", got)
	}

	demo := unitFile("/usr/local/bin/pine", ":9000", "/data", true, "", "", false)
	if !strings.Contains(demo, `--data "/data" --demo`) {
		t.Errorf("--demo not appended:\n%s", demo)
	}
	// A loopback install with no label carries no PINE_LABEL env.
	if strings.Contains(got, "PINE_LABEL") {
		t.Errorf("unit should carry no label env when none set:\n%s", got)
	}
}

func TestUnitFileToken(t *testing.T) {
	got := unitFile("/usr/local/bin/pine", ":8743", "/data", false, "s3cr3t", "", false)
	// The token rides in the environment, never on the command line (which `ps`
	// exposes to every local user).
	if !strings.Contains(got, "Environment=PINE_TOKEN=s3cr3t") {
		t.Errorf("token not carried via environment:\n%s", got)
	}
	if strings.Contains(got, "--token") || strings.Contains(got, "s3cr3t\" serve") {
		t.Errorf("token leaked onto the command line:\n%s", got)
	}
}

func TestUnitFileInsecure(t *testing.T) {
	got := unitFile("/usr/local/bin/pine", ":8743", "/data", false, "", "", true)
	if !strings.Contains(got, "serve --addr :8743 --data \"/data\" --insecure") {
		t.Errorf("--insecure not passed through:\n%s", got)
	}
	if strings.Contains(got, "PINE_TOKEN") {
		t.Errorf("insecure unit should carry no token:\n%s", got)
	}
}

func TestUnitFileLabel(t *testing.T) {
	got := unitFile("/usr/local/bin/pine", ":8743", "/data", false, "", "iba", false)
	// The label rides in the environment (PINE_LABEL), like the token, and never
	// on the command line — keeping ExecStart stable.
	if !strings.Contains(got, "Environment=PINE_LABEL=iba") {
		t.Errorf("label not carried via environment:\n%s", got)
	}
	if strings.Contains(got, "--label") {
		t.Errorf("label leaked onto the command line:\n%s", got)
	}
}
