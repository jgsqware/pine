package main

import (
	"strings"
	"testing"
)

func TestUnitFile(t *testing.T) {
	got := unitFile("/usr/local/bin/pine", ":8743", "/home/u/.pine", false)

	want := `ExecStart="/usr/local/bin/pine" serve --addr :8743 --data "/home/u/.pine"`
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

	demo := unitFile("/usr/local/bin/pine", ":9000", "/data", true)
	if !strings.Contains(demo, `--data "/data" --demo`) {
		t.Errorf("--demo not appended:\n%s", demo)
	}
}
