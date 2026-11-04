// Package compose checks the rendered Docker Compose files with the real
// docker compose CLI, as an operator would run them.
package compose

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

type rendered struct {
	Services map[string]struct {
		Environment map[string]string `json:"environment"`
		Ports       []struct {
			HostIP    string `json:"host_ip"`
			Published string `json:"published"`
			Target    int    `json:"target"`
		} `json:"ports"`
		Volumes []struct {
			Source   string `json:"source"`
			Target   string `json:"target"`
			ReadOnly bool   `json:"read_only"`
		} `json:"volumes"`
	} `json:"services"`
}

func render(t *testing.T, kubeconfig string, files ...string) (rendered, string, error) {
	t.Helper()
	env := map[string]string{}
	if kubeconfig != "" {
		env["KUBECONFIG_FILE"] = kubeconfig
	}
	return renderEnv(t, env, files...)
}

func renderEnv(t *testing.T, env map[string]string, files ...string) (rendered, string, error) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI not installed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	args := []string{"compose", "--env-file", os.DevNull}
	for _, f := range files {
		args = append(args, "-f", f)
	}
	cmd := exec.CommandContext(ctx, "docker", append(args, "config", "--format", "json")...)
	cmd.Dir = "../.."
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); name != "KUBECONFIG_FILE" && name != "EXTERNAL_DATABASE_URL" && name != "POSTGRES_PORT" {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	var r rendered
	if err == nil {
		err = json.Unmarshal(out, &r)
	}
	return r, stderr.String(), err
}

func TestLiveOverrideMountsKubeconfigReadOnly(t *testing.T) {
	r, stderr, err := render(t, "/srv/pilot/kubeconfig", "docker-compose.yml", "docker-compose.live.yml")
	if err != nil {
		t.Fatalf("render: %v %s", err, stderr)
	}
	app := r.Services["app"]
	if app.Environment["SIMULATION_ENABLED"] != "false" || app.Environment["KUBECONFIG"] != "/kube/config" {
		t.Fatalf("live environment: %v", app.Environment)
	}
	mounted := false
	for _, v := range app.Volumes {
		mounted = mounted || (v.Source == "/srv/pilot/kubeconfig" && v.Target == "/kube/config" && v.ReadOnly)
	}
	if !mounted {
		t.Fatalf("kubeconfig is not mounted read-only: %+v", app.Volumes)
	}
}

func TestLiveOverrideRequiresKubeconfigFile(t *testing.T) {
	if _, stderr, err := render(t, "", "docker-compose.yml", "docker-compose.live.yml"); err == nil || !strings.Contains(stderr, "set KUBECONFIG_FILE") {
		t.Fatalf("missing KUBECONFIG_FILE must fail clearly: %v %s", err, stderr)
	}
}

func TestDefaultComposeStaysInSimulation(t *testing.T) {
	r, stderr, err := render(t, "", "docker-compose.yml")
	if err != nil {
		t.Fatalf("render: %v %s", err, stderr)
	}
	if app := r.Services["app"]; app.Environment["SIMULATION_ENABLED"] != "true" || len(app.Volumes) != 0 {
		t.Fatalf("default compose changed: %+v", app)
	}
}

func TestDatabaseDefaultsToBundledPostgresOnLoopback(t *testing.T) {
	r, stderr, err := renderEnv(t, nil, "docker-compose.yml")
	if err != nil {
		t.Fatalf("render: %v %s", err, stderr)
	}
	if got := r.Services["app"].Environment["DATABASE_URL"]; !strings.Contains(got, "@postgres:5432/") {
		t.Fatalf("default database: %q", got)
	}
	ports := r.Services["postgres"].Ports
	if len(ports) != 1 || ports[0].HostIP != "127.0.0.1" || ports[0].Published != "5432" || ports[0].Target != 5432 {
		t.Fatalf("PostgreSQL must be published on loopback only: %+v", ports)
	}
	r, stderr, err = renderEnv(t, map[string]string{"POSTGRES_PORT": "55432"}, "docker-compose.yml")
	if err != nil || r.Services["postgres"].Ports[0].Published != "55432" {
		t.Fatalf("POSTGRES_PORT not applied: %v %s %+v", err, stderr, r.Services["postgres"].Ports)
	}
}

func TestExternalDatabaseURLReplacesBundledPostgres(t *testing.T) {
	const url = "postgres://troubleshooter:secret@db.example.net:5432/troubleshooter?sslmode=require"
	r, stderr, err := renderEnv(t, map[string]string{"EXTERNAL_DATABASE_URL": url, "KUBECONFIG_FILE": "/srv/k"}, "docker-compose.yml", "docker-compose.live.yml")
	if err != nil {
		t.Fatalf("render: %v %s", err, stderr)
	}
	if got := r.Services["app"].Environment["DATABASE_URL"]; got != url {
		t.Fatalf("app database: %q", got)
	}
}
