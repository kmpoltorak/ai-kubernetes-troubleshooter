// Package grafana holds tests for the Grafana dashboard that operators import.
package grafana

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestDashboardUsesExportedMetrics fails when a panel queries a metric the
// application does not register, e.g. after a rename.
func TestDashboardUsesExportedMetrics(t *testing.T) {
	src, err := os.ReadFile("../../internal/observability/metrics.go")
	if err != nil {
		t.Fatal(err)
	}
	exported := map[string]bool{}
	for _, m := range regexp.MustCompile(`Name: "([a-z_]+)"`).FindAllStringSubmatch(string(src), -1) {
		for _, suffix := range []string{"", "_bucket", "_sum", "_count"} {
			exported[m[1]+suffix] = true
		}
	}

	raw, err := os.ReadFile("dashboards/kubernetes-troubleshooter.json")
	if err != nil {
		t.Fatal(err)
	}
	var dash struct {
		Panels []struct {
			Title   string `json:"title"`
			Targets []struct {
				Expr string `json:"expr"`
			} `json:"targets"`
		} `json:"panels"`
	}
	if err := json.Unmarshal(raw, &dash); err != nil {
		t.Fatal(err)
	}
	metric := regexp.MustCompile(`\b[a-z][a-z_]*_(total|seconds_bucket|seconds_sum|seconds_count)\b`)
	queries := 0
	for _, p := range dash.Panels {
		for _, tgt := range p.Targets {
			queries++
			found := metric.FindAllString(tgt.Expr, -1)
			if len(found) == 0 {
				t.Errorf("panel %q: no metric recognized in %q", p.Title, tgt.Expr)
			}
			for _, m := range found {
				if !exported[m] && !strings.HasPrefix(m, "__") {
					t.Errorf("panel %q queries unknown metric %s", p.Title, m)
				}
			}
		}
	}
	if queries < 15 {
		t.Fatalf("only %d queries found; dashboard not parsed correctly", queries)
	}
}

// TestDashboardImportsIntoAnyGrafana fails when a panel or query is tied to a
// data source that exists only in one Grafana instead of the selectable variable.
func TestDashboardImportsIntoAnyGrafana(t *testing.T) {
	raw, err := os.ReadFile("dashboards/kubernetes-troubleshooter.json")
	if err != nil {
		t.Fatal(err)
	}
	type source struct{ Type, UID string }
	var dash struct {
		Templating struct {
			List []struct{ Name, Type, Query string } `json:"list"`
		} `json:"templating"`
		Panels []struct {
			Title      string  `json:"title"`
			Type       string  `json:"type"`
			Datasource *source `json:"datasource"`
			Targets    []struct {
				Datasource *source `json:"datasource"`
			} `json:"targets"`
		} `json:"panels"`
	}
	if err := json.Unmarshal(raw, &dash); err != nil {
		t.Fatal(err)
	}
	variable := false
	for _, v := range dash.Templating.List {
		variable = variable || (v.Name == "datasource" && v.Type == "datasource" && v.Query == "prometheus")
	}
	if !variable {
		t.Fatal("dashboard needs a Prometheus data source variable named datasource")
	}
	want := source{Type: "prometheus", UID: "${datasource}"}
	for _, p := range dash.Panels {
		if p.Type == "row" {
			continue
		}
		if p.Datasource == nil || *p.Datasource != want {
			t.Errorf("panel %q data source %+v, want the variable", p.Title, p.Datasource)
		}
		for _, tgt := range p.Targets {
			if tgt.Datasource == nil || *tgt.Datasource != want {
				t.Errorf("panel %q query data source %+v, want the variable", p.Title, tgt.Datasource)
			}
		}
	}
}
