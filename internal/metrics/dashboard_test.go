package metrics

import (
	"encoding/json"
	"maps"
	"os"
	"regexp"
	"slices"
	"testing"
)

// dashboardPath is the dashboard the README points at.
const dashboardPath = "../../deploy/grafana/dashboard.json"

// dashboardMetricRef matches a Prometheus metric name inside a PromQL expr.
var dashboardMetricRef = regexp.MustCompile(`\b(xray_[a-z0-9_]+)\b`)

// dashboardDoc is the part of Grafana's dashboard model the drift test reads.
type dashboardDoc struct {
	SchemaVersion int `json:"schemaVersion"`
	Templating    struct {
		List []dashboardVariable `json:"list"`
	} `json:"templating"`
}

type dashboardVariable struct {
	Name       string          `json:"name"`
	Type       string          `json:"type"`
	Query      json.RawMessage `json:"query"`
	Multi      bool            `json:"multi"`
	IncludeAll bool            `json:"includeAll"`
}

// queryString is a variable's query, whether Grafana stores it as a bare
// string (datasource) or as a definition string (query variables).
func (v dashboardVariable) queryString() string {
	var query string
	if err := json.Unmarshal(v.Query, &query); err != nil {
		return ""
	}
	return query
}

// TestDashboardMetricsExist is the only thing between a metric rename and a
// silently empty panel: it fails when the dashboard names a metric the exporter
// does not register, or when the importable shape regresses.
func TestDashboardMetricsExist(t *testing.T) {
	raw, err := os.ReadFile(dashboardPath)
	if err != nil {
		t.Fatalf("read dashboard: %v", err)
	}

	var doc dashboardDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse dashboard: %v", err)
	}
	if doc.SchemaVersion < 39 {
		t.Errorf("schemaVersion = %d, want at least 39", doc.SchemaVersion)
	}

	checkDashboardVariables(t, doc.Templating.List)

	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("parse dashboard document: %v", err)
	}
	exprs, panels := dashboardPanels(document)
	if panels < 10 {
		t.Errorf("dashboard has %d panels, want at least 10", panels)
	}
	if len(exprs) == 0 {
		t.Fatal("dashboard has no panel expressions")
	}
	if unknown := unknownDashboardMetrics(exprs); len(unknown) > 0 {
		t.Errorf("dashboard references metrics the exporter does not register: %v", unknown)
	}

	t.Run("an unregistered metric is reported", func(t *testing.T) {
		var synthetic any
		if err := json.Unmarshal([]byte(`{"panels":[{"type":"timeseries","targets":[{"expr":"rate(xray_not_a_metric_total[5m])"}]}]}`), &synthetic); err != nil {
			t.Fatalf("parse synthetic dashboard: %v", err)
		}
		exprs, _ := dashboardPanels(synthetic)
		if unknown := unknownDashboardMetrics(exprs); !slices.Contains(unknown, "xray_not_a_metric_total") {
			t.Errorf("drift check did not flag xray_not_a_metric_total, got %v", unknown)
		}
	})
}

// checkDashboardVariables pins the datasource variable and the instance →
// config → target chain, so the dashboard stays importable with no baked UID.
func checkDashboardVariables(t *testing.T, list []dashboardVariable) {
	t.Helper()

	variables := make(map[string]dashboardVariable, len(list))
	for _, v := range list {
		variables[v.Name] = v
	}

	want := map[string]struct {
		kind  string
		query string
	}{
		"datasource": {kind: "datasource", query: "prometheus"},
		"instance":   {kind: "query", query: `label_values(xray_config_running, instance)`},
		"config":     {kind: "query", query: `label_values(xray_config_running{instance=~"$instance"}, config)`},
		"target":     {kind: "query", query: `label_values(xray_probe_up{instance=~"$instance",config=~"$config"}, target)`},
	}

	for name, expected := range want {
		v, ok := variables[name]
		if !ok {
			t.Errorf("dashboard has no %q variable", name)
			continue
		}
		if v.Type != expected.kind {
			t.Errorf("variable %q has type %q, want %q", name, v.Type, expected.kind)
		}
		if got := v.queryString(); got != expected.query {
			t.Errorf("variable %q has query %q, want %q", name, got, expected.query)
		}
		if expected.kind == "query" && (!v.Multi || !v.IncludeAll) {
			t.Errorf("variable %q is multi=%t includeAll=%t, want both true", name, v.Multi, v.IncludeAll)
		}
	}
}

// unknownDashboardMetrics returns every metric an expression names that
// AllNames does not declare, in document order.
func unknownDashboardMetrics(exprs []string) []string {
	known := make(map[string]struct{}, len(AllNames()))
	for _, name := range AllNames() {
		known[name] = struct{}{}
	}

	var unknown []string
	for _, expr := range exprs {
		for _, match := range dashboardMetricRef.FindAllStringSubmatch(expr, -1) {
			name := match[1]
			if _, ok := known[name]; ok || slices.Contains(unknown, name) {
				continue
			}
			unknown = append(unknown, name)
		}
	}
	return unknown
}

// dashboardPanels returns every panel expression and how many panels hold
// targets. Rows carry no targets, so they do not count.
func dashboardPanels(document any) (exprs []string, panels int) {
	for _, obj := range dashboardObjects(document, nil) {
		if expr, ok := obj["expr"].(string); ok {
			exprs = append(exprs, expr)
		}
		if _, ok := obj["targets"].([]any); ok {
			if kind, _ := obj["type"].(string); kind != "" && kind != "row" {
				panels++
			}
		}
	}
	return exprs, panels
}

// dashboardObjects flattens every JSON object in the document, visiting keys in
// sorted order so a failure lists metrics deterministically.
func dashboardObjects(node any, out []map[string]any) []map[string]any {
	switch value := node.(type) {
	case map[string]any:
		out = append(out, value)
		for _, key := range slices.Sorted(maps.Keys(value)) {
			out = dashboardObjects(value[key], out)
		}
	case []any:
		for _, child := range value {
			out = dashboardObjects(child, out)
		}
	}
	return out
}
