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

func TestReleasePublishesOnlyAfterAllChecks(t *testing.T) {
	w := readWorkflow(t, "release")
	push, _ := w.On["push"].(map[string]any)
	if len(w.On) != 1 || push == nil || len(w.Permissions) != 1 || w.Permissions["contents"] != "read" {
		t.Fatal("release must run only on push, read-only by default")
	}
	if branches, _ := push["branches"].([]any); len(branches) != 1 || branches[0] != "main" {
		t.Fatal("release must run only from main")
	}
	rp := w.Jobs["release-please"]
	if len(rp.Permissions) != 3 || rp.Permissions["contents"] != "write" || rp.Permissions["pull-requests"] != "write" || rp.Permissions["issues"] != "write" {
		t.Fatalf("release-please permissions: %v", rp.Permissions)
	}
	if w.Jobs["validate"].If != "needs.release-please.outputs.created == 'true'" {
		t.Fatal("validation must run only for a created release")
	}
	checks := w.Jobs["checks"]
	if checks.Uses != "./.github/workflows/ci.yml" || !needs(checks.Needs, "validate") {
		t.Fatal("release does not use the validated full CI workflow")
	}
	publish := w.Jobs["publish"]
	if !needs(publish.Needs, "checks") || len(publish.Permissions) != 2 || publish.Permissions["packages"] != "write" || publish.Permissions["contents"] != "write" {
		t.Fatal("publication bypasses the tested candidate or has extra permissions")
	}
	for name, job := range w.Jobs {
		if name != "publish" && name != "release-please" && len(job.Permissions) != 0 {
			t.Fatalf("unexpected permission override: %s", name)
		}
	}
	var publication strings.Builder
	for _, step := range publish.Steps {
		publication.WriteString(step.Run)
	}
	script := publication.String()
	order := []string{
		`test "$(gh release view "v$VERSION" --json isDraft --jq .isDraft)" = true`,
		`bash scripts/image-artifact.sh publish candidate "$repository"`,
		`gh release upload "v$VERSION"`,
		`gh release edit "v$VERSION" --notes-file "$work/notes.md" --draft=false`,
	}
	last := -1
	for _, want := range order {
		i := strings.Index(script, want)
		if i <= last {
			t.Fatalf("publication must check the draft, push the image, attach assets, then publish: %q", want)
		}
		last = i
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

func needs(value any, job string) bool {
	switch v := value.(type) {
	case string:
		return v == job
	case []any:
		for _, n := range v {
			if n == job {
				return true
			}
		}
	}
	return false
}

// Draft releases get their tag at once, so a failed check leaves no public release.
func TestReleasePleaseCreatesDraftsWithPlainTags(t *testing.T) {
	raw, err := os.ReadFile("../../release-please-config.json")
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["draft"] != true || cfg["force-tag-creation"] != true || cfg["include-component-in-tag"] != false {
		t.Fatalf("release-please must create draft releases tagged vX.Y.Z at once: %v", cfg)
	}
}

func TestReleaseVersionGuard(t *testing.T) {
	guard := readWorkflow(t, "release").Jobs["validate"].Steps[0].Run
	const sha = "92023c917abb39e8da18ba6c40be3a88dc1a9e4a"
	for _, tc := range []struct {
		version, releaseSHA string
		ok                  bool
	}{{"1.0.0", sha, true}, {"1.12.3", sha, true}, {"2.0.0", sha, true},
		{"1.0.0-rc.1", sha, false}, {"v1.0.0", sha, false}, {"01.0.0", sha, false}, {"1.0", sha, false},
		{"$(exit 0)", sha, false}, {"", sha, false}, {"1.0.0", "0000000000000000000000000000000000000000", false}} {
		t.Run(tc.version+"/"+tc.releaseSHA[:7], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bash", "-e", "-o", "pipefail", "-c", guard)
			cmd.Env = append(os.Environ(), "VERSION="+tc.version, "RELEASE_SHA="+tc.releaseSHA, "GITHUB_SHA="+sha)
			output, err := cmd.CombinedOutput()
			if (err == nil) != tc.ok {
				t.Fatalf("unexpected guard result: %v %s", err, strings.TrimSpace(string(output)))
			}
		})
	}
}
