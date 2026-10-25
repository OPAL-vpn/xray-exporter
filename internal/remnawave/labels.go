package remnawave

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var (
	labelDisallowed = regexp.MustCompile(`[^a-z0-9_-]+`)
	labelDashRun    = regexp.MustCompile(`-{2,}`)
	labelShape      = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
)

// sanitizeLabel turns a panel string into a metric-label value. It returns ""
// when nothing usable is left, so the caller can fall back.
func sanitizeLabel(s string) string {
	s = strings.ToLower(s)
	s = labelDisallowed.ReplaceAllString(s, "-")
	s = labelDashRun.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-_")
	if !labelShape.MatchString(s) {
		return ""
	}
	return s
}

// labelsFor names every entry in entry order: its remarks, else its first
// outbound endpoint, else "config-<N>". A repeated label gets "-2", "-3", …
// against the labels already handed out. Labels never fail a fetch.
func labelsFor(entries []json.RawMessage) []string {
	labels := make([]string, len(entries))
	used := make(map[string]bool, len(entries))
	for i, entry := range entries {
		label := sanitizeLabel(remark(entry))
		if label == "" {
			label = sanitizeLabel(endpoint(entry))
		}
		if label == "" {
			label = fmt.Sprintf("config-%d", i+1)
		}

		unique := label
		for n := 2; used[unique]; n++ {
			unique = fmt.Sprintf("%s-%d", label, n)
		}
		used[unique] = true
		labels[i] = unique
	}
	return labels
}
