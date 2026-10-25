package remnawave

import (
	"bytes"
	"encoding/json"
	"testing"
)

// rawJSON turns a test JSON literal into the raw message parsePayload returns.
func rawJSON(t *testing.T, body string) json.RawMessage {
	t.Helper()
	if !json.Valid([]byte(body)) {
		t.Fatalf("test literal is not valid JSON: %s", body)
	}
	return json.RawMessage(body)
}

func TestParseArray(t *testing.T) {
	body := []byte(`[{"a":1},{"b":2}]`)

	entries, err := parsePayload(body)
	if err != nil {
		t.Fatalf("parsePayload: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}
	if got, want := string(entries[0]), `{"a":1}`; got != want {
		t.Errorf("entries[0] = %q, want %q", got, want)
	}
	if got, want := string(entries[1]), `{"b":2}`; got != want {
		t.Errorf("entries[1] = %q, want %q", got, want)
	}
}

func TestParseEmptyArray(t *testing.T) {
	entries, err := parsePayload([]byte("[]"))
	if err != nil {
		t.Fatalf("parsePayload: %v", err)
	}
	if entries == nil {
		t.Fatal("parsePayload: got a nil slice, want a non-nil empty slice")
	}
	if len(entries) != 0 {
		t.Errorf("len(entries) = %d, want 0", len(entries))
	}
}

func TestParseRejectsNonArray(t *testing.T) {
	cases := []struct{ name, body string }{
		{"null", "null"},
		{"object", `{"x":1}`},
		{"string", `"x"`},
		{"number", "123"},
		{"bool", "true"},
		{"empty body", ""},
		{"blank body", "  \n\t "},
		{"truncated array", `[{"a":1}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entries, err := parsePayload([]byte(tc.body))
			if err == nil {
				t.Fatalf("parsePayload(%q) = %d entries, want an error", tc.body, len(entries))
			}
			if bytes.Contains([]byte(err.Error()), []byte(tc.body)) && tc.body != "" {
				t.Errorf("error = %q, want it to omit the response body", err)
			}
		})
	}
}

func TestCanonicalIsStable(t *testing.T) {
	compact := rawJSON(t, `{"b":2,"a":1}`)
	spaced := rawJSON(t, "{\n\t\"a\" : 1 ,\n\t\"b\" : 2\n}")
	reordered := rawJSON(t, `{"a":1,"b":2}`)

	want, err := canonical(reordered)
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	if got, wantBody := string(want), `{"a":1,"b":2}`; got != wantBody {
		t.Errorf("canonical = %q, want %q", got, wantBody)
	}
	for name, raw := range map[string]json.RawMessage{"compact": compact, "spaced": spaced} {
		got, err := canonical(raw)
		if err != nil {
			t.Fatalf("canonical(%s): %v", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("canonical(%s) = %q, want %q", name, got, want)
		}
	}
}

func TestRemarkAndEndpointExtraction(t *testing.T) {
	cases := []struct {
		name         string
		body         string
		wantRemark   string
		wantEndpoint string
	}{
		{
			name:       "remarks",
			body:       `{"remarks":"Germany | 1","outbounds":[]}`,
			wantRemark: "Germany | 1",
		},
		{
			name:         "vnext",
			body:         `{"outbounds":[{"settings":{"vnext":[{"address":"de1.example","port":443}]}}]}`,
			wantEndpoint: "de1.example-443",
		},
		{
			name:         "servers",
			body:         `{"outbounds":[{"settings":{"servers":[{"address":"nl1.example","port":80}]}}]}`,
			wantEndpoint: "nl1.example-80",
		},
		{
			name:       "remarks and vnext",
			body:       `{"remarks":"lt1","outbounds":[{"settings":{"vnext":[{"address":"de1.example","port":443}]}}]}`,
			wantRemark: "lt1", wantEndpoint: "de1.example-443",
		},
		{
			name: "missing",
			body: `{}`,
		},
		{
			name: "non-object",
			body: `"x"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entry := rawJSON(t, tc.body)
			if got := remark(entry); got != tc.wantRemark {
				t.Errorf("remark() = %q, want %q", got, tc.wantRemark)
			}
			if got := endpoint(entry); got != tc.wantEndpoint {
				t.Errorf("endpoint() = %q, want %q", got, tc.wantEndpoint)
			}
		})
	}
}
