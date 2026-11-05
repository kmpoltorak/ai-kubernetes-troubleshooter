package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/config"
)

func TestDocsServeEveryGuideWithWorkingLinks(t *testing.T) {
	srv := httptest.NewServer(New(nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), 1000, config.Auth{}, Runtime{}, nil))
	t.Cleanup(srv.Close)
	fetch := func(path string) (int, string, http.Header) {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp.StatusCode, string(body), resp.Header
	}
	code, index, header := fetch("/docs/")
	if code != 200 || !strings.Contains(index, `<a href="/docs/clusters">Several clusters</a>`) || header.Get("Content-Security-Policy") == "" {
		t.Fatalf("index: %d %s", code, index)
	}
	pages := regexp.MustCompile(`href="/docs/([a-z-]+)"`).FindAllStringSubmatch(index, -1)
	if len(pages) < 12 {
		t.Fatalf("index lists %d guides", len(pages))
	}
	// Every link between guides must reach a served page and, with an anchor, a heading on it.
	link := regexp.MustCompile(`href="/docs/([a-z-]+)(?:#([^"]+))?"`)
	for _, p := range pages {
		code, body, _ := fetch("/docs/" + p[1])
		if code != 200 {
			t.Fatalf("%s: %d", p[1], code)
		}
		if regexp.MustCompile(`href="[^":#/][^":]*\.md`).MatchString(body) {
			t.Errorf("%s keeps a relative .md link", p[1])
		}
		for _, l := range link.FindAllStringSubmatch(body, -1) {
			code, target, _ := fetch("/docs/" + l[1])
			if code != 200 || (l[2] != "" && !strings.Contains(target, `id="`+l[2]+`"`)) {
				t.Errorf("%s links to missing /docs/%s#%s", p[1], l[1], l[2])
			}
		}
	}
	if code, body, _ := fetch("/docs/clusters"); !strings.Contains(body, `href="`+repoURL+`deployments/kubernetes/rbac.yaml"`) {
		t.Fatalf("link outside docs not sent to the repository: %d", code)
	}
	if code, _, _ := fetch("/docs/plan-multi-cluster"); code != 404 {
		t.Fatalf("unknown guide: %d", code)
	}
	if code, css, _ := fetch("/docs/style.css"); code != 200 || !strings.Contains(css, "prefers-color-scheme") {
		t.Fatalf("style: %d", code)
	}
}

func TestRenderDocsDropsRawHTML(t *testing.T) {
	pages, _ := renderDocs(fstest.MapFS{"x.md": {Data: []byte("# X\n\n<script>alert(1)</script>\n\n[y](y.md#part) [r](../README.md)\n")}})
	got := string(pages["x"])
	if strings.Contains(got, "<script>alert") || !strings.Contains(got, `href="/docs/y#part"`) || !strings.Contains(got, `href="`+repoURL+`README.md"`) {
		t.Fatalf("rendered: %s", got)
	}
}
