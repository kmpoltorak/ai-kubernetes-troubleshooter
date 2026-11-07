package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/llm"
)

func TestStartupModelCheck(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		fmt.Fprint(w, `{"data":[{"id":"gpt-6-luna"}]}`)
	}))
	defer srv.Close()
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	p := llm.NewOpenAIProvider(srv.URL, "k", "m", "", time.Second)
	if err := checkModel(context.Background(), log, p, "gpt-typo"); !errors.Is(err, llm.ErrModelUnavailable) {
		t.Fatalf("unknown model did not stop startup: %v", err)
	}
	status = http.StatusServiceUnavailable
	if err := checkModel(context.Background(), log, p, "gpt-6-luna"); err != nil || !strings.Contains(logs.String(), "could not check LLM_MODEL") {
		t.Fatalf("outage stopped startup or was not logged: %v, %s", err, logs.String())
	}
}

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
