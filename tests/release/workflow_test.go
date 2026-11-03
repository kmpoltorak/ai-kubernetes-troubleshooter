package release

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/yaml"
)

type workflow struct {
	On          map[string]any    `json:"on"`
	Permissions map[string]string `json:"permissions"`
	Jobs        map[string]struct {
		Uses        string            `json:"uses"`
		If          string            `json:"if"`
		Needs       any               `json:"needs"`
		Permissions map[string]string `json:"permissions"`
		Steps       []struct {
			Run string `json:"run"`
		} `json:"steps"`
	} `json:"jobs"`
}

func readWorkflow(t *testing.T, name string) workflow {
	t.Helper()
	raw, err := os.ReadFile("../../.github/workflows/" + name + ".yml")
	if err != nil {
		t.Fatal(err)
	}
	var w workflow
	if err := yaml.Unmarshal(raw, &w); err != nil {
		t.Fatal(err)
	}
	return w
}

func TestPublicationRequiresManualOptInAndAllChecks(t *testing.T) {
	w := readWorkflow(t, "release")
	if len(w.On) != 1 || w.On["workflow_dispatch"] == nil || w.Permissions["contents"] != "read" || len(w.Permissions) != 1 {
		t.Fatal("release is not manual/read-only by default")
	}
	inputs := w.On["workflow_dispatch"].(map[string]any)["inputs"].(map[string]any)
	if inputs["publish"].(map[string]any)["default"] != false {
		t.Fatal("publication must default to disabled")
	}
	publish := w.Jobs["publish"]
	if publish.If != "${{ inputs.publish }}" || publish.Needs != "checks" || publish.Permissions["packages"] != "write" || publish.Permissions["contents"] != "write" {
		t.Fatal("publication bypasses opt-in or tested candidate")
	}
	for name, job := range w.Jobs {
		if name != "publish" && len(job.Permissions) != 0 {
			t.Fatalf("unexpected permission override: %s", name)
		}
	}
	var publication strings.Builder
	for _, step := range publish.Steps {
		publication.WriteString(step.Run)
	}
	if !strings.Contains(publication.String(), `gh api --method POST "repos/$GITHUB_REPOSITORY/git/refs"`) ||
		!strings.Contains(publication.String(), `-f "ref=refs/tags/v$VERSION" -f "sha=$GITHUB_SHA"`) ||
		!strings.Contains(publication.String(), `gh release create "v$VERSION" --verify-tag`) {
		t.Fatal("release tag must be created at the tested commit, without reusing a racing tag")
	}
	if w.Jobs["checks"].Uses != "./.github/workflows/ci.yml" || w.Jobs["checks"].Needs != "validate" {
		t.Fatal("release does not use the validated full CI workflow")
	}
	ci := readWorkflow(t, "ci")
	if ci.On["workflow_call"] == nil || len(ci.Permissions) != 1 || ci.Permissions["contents"] != "read" {
		t.Fatal("CI is not reusable/read-only")
	}
	for _, name := range []string{"lint", "test", "web", "security", "authentication", "integration", "build", "kubernetes"} {
		job, ok := ci.Jobs[name]
		if !ok || len(job.Permissions) != 0 {
			t.Fatalf("required CI job missing or privileged: %s", name)
		}
	}
}

func TestReleaseInputGuard(t *testing.T) {
	guard := readWorkflow(t, "release").Jobs["validate"].Steps[0].Run
	for _, tc := range []struct {
		version, ref string
		ok           bool
	}{{"0.1.0", "refs/heads/main", true}, {"0.0.0", "refs/heads/main", true},
		{"1.0.0", "refs/heads/main", false}, {"0.01.0", "refs/heads/main", false},
		{"0.1.0", "refs/heads/other", false}, {"0.1.0", "refs/tags/v0.1.0", false},
		{"$(exit 0)", "refs/heads/main", false}, {"", "refs/heads/main", false}} {
		t.Run(tc.version+"/"+tc.ref, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bash", "-e", "-o", "pipefail", "-c", guard)
			cmd.Env = append(os.Environ(), "VERSION="+tc.version, "GITHUB_REF="+tc.ref)
			output, err := cmd.CombinedOutput()
			if (err == nil) != tc.ok {
				t.Fatalf("unexpected guard result: %v %s", err, strings.TrimSpace(string(output)))
			}
		})
	}
}
