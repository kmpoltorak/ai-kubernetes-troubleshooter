package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestStartupMasksErrors(t *testing.T) {
	if os.Getenv("TEST_STARTUP_CHILD") == "1" {
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestStartupMasksErrors$")
	cmd.Env = []string{"TEST_STARTUP_CHILD=1", "DATABASE_URL=postgres://operator:synthetic-startup@example.com/db?sslmode=invalid"}
	output, err := cmd.CombinedOutput()
	if err == nil || strings.Contains(string(output), "synthetic-startup") || !strings.Contains(string(output), "error:") || !strings.Contains(string(output), "sslmode") {
		t.Fatalf("unsafe startup error: %s (%v)", output, err)
	}
}
