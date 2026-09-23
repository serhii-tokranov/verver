package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		code int
		want string
	}{
		{"no arguments", nil, 0, "Usage:"},
		{"help", []string{"--help"}, 0, "Usage:"},
		{"short help", []string{"-h"}, 0, "Usage:"},
		{"version", []string{"--version"}, 0, "verver 1.2.3\n"},
		{"unknown flag", []string{"--unknown"}, 2, ""},
		{"unknown command", []string{"next"}, 2, ""},
		{"extra argument", []string{"--version", "extra"}, 2, ""},
		{"conflicting flags", []string{"--help", "--version"}, 2, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := Run(tt.args, &stdout, &stderr, "1.2.3"); code != tt.code {
				t.Fatalf("exit code = %d, want %d", code, tt.code)
			}
			if tt.code == 0 {
				if !strings.Contains(stdout.String(), tt.want) || stderr.Len() != 0 {
					t.Fatalf("unexpected streams: stdout=%q stderr=%q", stdout.String(), stderr.String())
				}
			} else if stdout.Len() != 0 || !strings.Contains(stderr.String(), "invalid arguments") {
				t.Fatalf("unexpected error streams: stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

func TestOutputFailure(t *testing.T) {
	var stderr bytes.Buffer
	if code := Run([]string{"--version"}, failingWriter{}, &stderr, "dev"); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "could not write output") {
		t.Fatalf("missing error: %q", stderr.String())
	}
}
