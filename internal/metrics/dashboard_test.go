package metrics

import (
	"encoding/json"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// dashboardPath is the dashboard the README points at.
const dashboardPath = "../../deploy/grafana/dashboard.json"

var dashboardMetricRef = regexp.MustCompile(`\b(xray_[a-z0-9_]+)\b`)

// dashboardPromQLKeys hold PromQL strings; a metric rename can drift in any.
var dashboardPromQLKeys = []string{"definition", "expr", "query"}

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

// queryString reads a query stored as a bare string, as Grafana does for
// datasource and some query variables.
func (v dashboardVariable) queryString() string {
	var query string
	if err := json.Unmarshal(v.Query, &query); err != nil {
		return ""
	}
	return query
}

// TestDashboardMetricsExist fails when a panel names a missing metric or the
// importable shape regresses.
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
	exprs, panelExprs, panels := dashboardPanels(document)
	if panels < 10 {
		t.Errorf("dashboard has %d panels, want at least 10", panels)
	}
	if len(exprs) == 0 {
		t.Fatal("dashboard has no PromQL strings")
	}
	if unknown := unknownDashboardMetrics(exprs); len(unknown) > 0 {
		t.Errorf("dashboard references metrics the exporter does not register: %v", unknown)
	}
	if unscoped := unscopedPanelExprs(panelExprs); len(unscoped) > 0 {
		t.Errorf("panel expressions ignore the $instance variable: %v", unscoped)
	}

	t.Run("an unregistered metric is reported", func(t *testing.T) {
		synthetic := `{
			"panels": [{"type": "timeseries", "targets": [{"expr": "rate(xray_not_a_metric_total[5m])"}]}],
			"templating": {"list": [{"name": "instance", "type": "query", "query": "label_values(xray_not_a_metric_series, instance)", "definition": "label_values(xray_not_a_metric_series, instance)"}]}
		}`
		var document any
		if err := json.Unmarshal([]byte(synthetic), &document); err != nil {
			t.Fatalf("parse synthetic dashboard: %v", err)
		}
		exprs, _, _ := dashboardPanels(document)
		unknown := unknownDashboardMetrics(exprs)
		for _, want := range []string{"xray_not_a_metric_total", "xray_not_a_metric_series"} {
			if !slices.Contains(unknown, want) {
				t.Errorf("drift check did not flag %s, got %v", want, unknown)
			}
		}
	})

	t.Run("an unscoped panel is reported", func(t *testing.T) {
		synthetic := []byte(`{"panels": [{"type": "stat", "targets": [{"expr": "sum by (instance) (xray_config_running)"}]}]}`)
		var document any
		if err := json.Unmarshal(synthetic, &document); err != nil {
			t.Fatalf("parse synthetic dashboard: %v", err)
		}
		_, panelExprs, _ := dashboardPanels(document)
		if unscoped := unscopedPanelExprs(panelExprs); len(unscoped) != 1 {
			t.Errorf("scoping check did not flag the unscoped panel, got %v", unscoped)
		}
	})
}

// checkDashboardVariables pins the instance → config → target chain.
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

// unscopedPanelExprs returns panel expressions that do not filter on $instance.
func unscopedPanelExprs(exprs []string) []string {
	var unscoped []string
	for _, expr := range exprs {
		if !strings.Contains(expr, "$instance") {
			unscoped = append(unscoped, expr)
		}
	}
	return unscoped
}

// dashboardPanels returns every PromQL string, the panel subset, and panels.
func dashboardPanels(document any) (exprs, panelExprs []string, panels int) {
	for _, obj := range dashboardObjects(document, nil) {
		for _, key := range dashboardPromQLKeys {
			if query, ok := obj[key].(string); ok {
				exprs = append(exprs, query)
			}
		}

		targets, ok := obj["targets"].([]any)
		if !ok {
			continue
		}
		if kind, _ := obj["type"].(string); kind != "" && kind != "row" {
			panels++
		}
		for _, target := range targets {
			targetObj, ok := target.(map[string]any)
			if !ok {
				continue
			}
			if expr, ok := targetObj["expr"].(string); ok {
				panelExprs = append(panelExprs, expr)
			}
		}
	}
	return exprs, panelExprs, panels
}

// dashboardObjects flattens every JSON object in sorted key order.
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
