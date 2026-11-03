//go:build e2e

package release

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

func TestReleaseManifestsPinImageWithoutSecrets(t *testing.T) {
	dir := t.TempDir()
	const digest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), []byte(`{"manifest_digest":"`+digest+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	output := filepath.Join(dir, "rendered")
	cmd := exec.CommandContext(ctx, "bash", "../../scripts/release-manifests.sh", dir, "ghcr.io/example/troubleshooter", output)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("render: %v %s", err, out)
	}
	for _, variant := range []string{"kubernetes", "kubernetes-authenticated"} {
		raw, err := os.ReadFile(filepath.Join(output, variant+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		decoder := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
		app, authSetting := false, false
		for {
			var obj unstructured.Unstructured
			if err := decoder.Decode(&obj); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				t.Fatal(err)
			}
			if obj.GetKind() == "Secret" {
				t.Fatal("release asset contains a Secret")
			}
			if obj.GetKind() == "Deployment" && obj.GetName() == "troubleshooter" {
				containers, _, err := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
				if err != nil || len(containers) != 1 || containers[0].(map[string]any)["image"] != "ghcr.io/example/troubleshooter@"+digest {
					t.Fatalf("application image is not pinned: %v", containers)
				}
				app = true
			}
			if obj.GetKind() == "ConfigMap" && obj.GetName() == "troubleshooter-config" {
				got, _, _ := unstructured.NestedString(obj.Object, "data", "AUTH_ENABLED")
				want := "false"
				if variant == "kubernetes-authenticated" {
					want = "true"
				}
				if got != want {
					t.Fatalf("authentication setting changed: %s", got)
				}
				authSetting = true
			}
		}
		if !app || !authSetting {
			t.Fatal("release omitted application or configuration")
		}
	}
}
