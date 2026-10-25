package remnawave

import (
	"encoding/json"
	"testing"
)

// remarkEntries builds one entry per remarks value, in order.
func remarkEntries(t *testing.T, remarks ...string) []json.RawMessage {
	t.Helper()
	entries := make([]json.RawMessage, 0, len(remarks))
	for _, r := range remarks {
		body, err := json.Marshal(map[string]string{"remarks": r})
		if err != nil {
			t.Fatalf("marshal remarks %q: %v", r, err)
		}
		entries = append(entries, body)
	}
	return entries
}

func TestSanitizeLabel(t *testing.T) {
	cases := []struct{ in, want string }{
		{"lt1", "lt1"},
		{"Germany | 1", "germany-1"},
		{"  --NL 2-- ", "nl-2"},
		{"A__B", "a__b"},
		{"🇩🇪", ""},
		{"Германия", ""},
		{" | ", ""},
	}
	for _, tc := range cases {
		if got := sanitizeLabel(tc.in); got != tc.want {
			t.Errorf("sanitizeLabel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestLabelFallsBackToEndpoint(t *testing.T) {
	entries := []json.RawMessage{
		rawJSON(t, `{"outbounds":[{"protocol":"vmess","settings":{"vnext":[{"address":"de1.example","port":443}]}}]}`),
	}

	labels := labelsFor(entries)
	if len(labels) != 1 {
		t.Fatalf("len(labels) = %d, want 1", len(labels))
	}
	if got, want := labels[0], "de1-example-443"; got != want {
		t.Errorf("labels[0] = %q, want %q", got, want)
	}
}

func TestLabelFallsBackToIndex(t *testing.T) {
	entries := append(
		remarkEntries(t, "lt1", "lt2"),
		rawJSON(t, `{"outbounds":[]}`),
	)

	labels := labelsFor(entries)
	if len(labels) != 3 {
		t.Fatalf("len(labels) = %d, want 3", len(labels))
	}
	if got, want := labels[2], "config-3"; got != want {
		t.Errorf("labels[2] = %q, want %q", got, want)
	}
}

func TestLabelsSuffixDuplicates(t *testing.T) {
	got := labelsFor(remarkEntries(t, "a", "a", "a"))
	want := []string{"a", "a-2", "a-3"}
	if len(got) != len(want) {
		t.Fatalf("len(labels) = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("labels[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestLabelsIncrementPastExistingSuffix(t *testing.T) {
	got := labelsFor(remarkEntries(t, "a", "a", "a-2"))
	want := []string{"a", "a-2", "a-2-2"}
	if len(got) != len(want) {
		t.Fatalf("len(labels) = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("labels[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
