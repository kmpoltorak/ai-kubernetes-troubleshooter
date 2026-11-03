package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunnerFailsClosedAndCleansOwnedCluster(t *testing.T) {
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"invalid_artifact", "list_failure", "create_failure"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			if err := os.Mkdir(bin, 0700); err != nil {
				t.Fatal(err)
			}
			trace := filepath.Join(dir, "trace")
			for _, tool := range []string{"kind", "docker", "kubectl", "go"} {
				body := `#!/bin/bash
printf '%s %s\n' "$(basename "$0")" "$*" >> "$E2E_TRACE"
if [ "$(basename "$0")" = kind ]; then
 if [ "$1" != version ]; then printf 'kubeconfig=%s\n' "$KUBECONFIG" >> "$E2E_TRACE"; fi
 case "$1" in
  version) echo 'kind v0.33.0 test';;
  get) if [ "$E2E_FAKE_MODE" = list_failure ]; then exit 7; fi;;
  create) exit 42;;
 esac
fi
`
				if err := os.WriteFile(filepath.Join(bin, tool), []byte(body), 0700); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			artifact := filepath.Join(dir, "report-fixture")
			if mode == "invalid_artifact" {
				artifact = filepath.Join(dir, "unsafe-name")
			}
			cmd := exec.CommandContext(ctx, "bash", filepath.Join(repo, "scripts/test-e2e.sh"))
			cmd.Env = append(os.Environ(), "PATH="+bin+":/usr/bin:/bin", "TMPDIR="+dir, "E2E_TRACE="+trace, "E2E_FAKE_MODE="+mode, "E2E_KUBERNETES_VERSION=1.37.0", "E2E_ARTIFACT_DIR="+artifact, "KUBECONFIG="+filepath.Join(dir, "user-context-must-stay"))
			if err := os.WriteFile(filepath.Join(dir, "user-context-must-stay"), []byte("unchanged"), 0600); err != nil {
				t.Fatal(err)
			}
			output, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatal("failed setup returned success")
			}
			commands, err := os.ReadFile(trace)
			if err != nil {
				t.Fatal(err)
			}
			text := string(commands)
			if strings.Contains(text, "kubeconfig="+filepath.Join(dir, "user-context-must-stay")) {
				t.Fatal("kind inherited the user's kubeconfig")
			}
			if mode == "create_failure" && !strings.Contains(text, "--kubeconfig ") {
				t.Fatal("kind did not receive explicit kubeconfig")
			}
			if mode != "create_failure" {
				if strings.Contains(text, "docker build") || strings.Contains(text, "kind delete") || strings.Contains(text, "kind create") {
					t.Fatalf("continued after failed inventory: %s", text)
				}
			} else if !strings.Contains(text, "kind create cluster --name akt-e2e-") || !strings.Contains(text, "kind delete cluster --name akt-e2e-") {
				t.Fatalf("partial cluster not cleaned: %s output=%s", text, output)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), "akt-e2e.") {
					t.Fatal("runner temporary directory leaked")
				}
			}
			original, err := os.ReadFile(filepath.Join(dir, "user-context-must-stay"))
			if err != nil || string(original) != "unchanged" {
				t.Fatal("user kubeconfig touched")
			}
		})
	}
}

func TestMakeLiveTargetDoesNotReadDotEnv(t *testing.T) {
	makefile, err := os.ReadFile("../../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), makefile, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("$(error dotenv must not be read by live tests)\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "make", "-n", "test-e2e")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "bash scripts/test-e2e.sh") {
		t.Fatalf("live target read dotenv: %s %v", out, err)
	}
	cmd = exec.CommandContext(ctx, "make", "-n", "run")
	cmd.Dir = dir
	if err := cmd.Run(); err == nil {
		t.Fatal("dotenv fixture cannot detect accidental loading")
	}
}
