package remnawave

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newSource builds a Source for a test panel URL.
func newSource(t *testing.T, rawURL string) *Source {
	t.Helper()
	src, err := New(rawURL, time.Minute)
	if err != nil {
		t.Fatalf("New(%q): %v", rawURL, err)
	}
	return src
}

func TestNewAppendsJSONOnce(t *testing.T) {
	cases := []struct{ name, rawURL, want string }{
		{"base", "https://panel.example", "https://panel.example/json"},
		{"trailing slash", "https://panel.example/", "https://panel.example/json"},
		{"already json", "https://panel.example/json", "https://panel.example/json"},
		{"already json slash", "https://panel.example/json/", "https://panel.example/json"},
		{"path", "https://panel.example/api/sub/abc", "https://panel.example/api/sub/abc/json"},
		{"query preserved", "https://sub.example/api/sub/abc?x=1", "https://sub.example/api/sub/abc/json?x=1"},
		{"query with trailing slash", "https://sub.example/api/sub/?x=1", "https://sub.example/api/sub/json?x=1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := newSource(t, tc.rawURL)
			if got := src.url.String(); got != tc.want {
				t.Errorf("url = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNewRejectsInvalidURL(t *testing.T) {
	const secret = "short-secret"
	cases := []struct{ name, rawURL string }{
		{"empty", ""},
		{"relative", "panel.example/" + secret},
		{"no host", "https:///" + secret},
		{"wrong scheme", "ftp://panel.example/" + secret},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src, err := New(tc.rawURL, time.Minute)
			if err == nil {
				t.Fatalf("New(%q) = %+v, want an error", tc.rawURL, src)
			}
			if strings.Contains(err.Error(), secret) {
				t.Errorf("error = %q, want it to omit the URL value", err)
			}
		})
	}
}

func TestKindAndRefresh(t *testing.T) {
	src := newSource(t, "https://panel.example")
	if got, want := src.Kind(), "remnawave"; got != want {
		t.Errorf("Kind() = %q, want %q", got, want)
	}
	if got, want := src.Refresh(), time.Minute; got != want {
		t.Errorf("Refresh() = %v, want %v", got, want)
	}
}

func TestFetchReturnsConfigs(t *testing.T) {
	const germany = `{"remarks":"Germany | 1","outbounds":[{"protocol":"vmess","settings":{"vnext":[{"address":"de1.example","port":443}]}}]}`

	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Method, http.MethodGet; got != want {
			t.Errorf("method = %q, want %q", got, want)
		}
		if got, want := r.URL.Path, "/api/sub/abc/json"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		if got, want := r.Header.Get("Accept"), "application/json"; got != want {
			t.Errorf("Accept = %q, want %q", got, want)
		}
		if got, want := r.Header.Get("User-Agent"), "xray-exporter"; got != want {
			t.Errorf("User-Agent = %q, want %q", got, want)
		}
		_, _ = io.WriteString(w, `[`+germany+`,{"outbounds":[{"settings":{"servers":[{"address":"nl1.example","port":8443}]}}]}]`)
	}))
	defer panel.Close()

	configs, err := newSource(t, panel.URL+"/api/sub/abc").Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(configs) != 2 {
		t.Fatalf("len(configs) = %d, want 2", len(configs))
	}
	if got, want := configs[0].Name, "germany-1"; got != want {
		t.Errorf("configs[0].Name = %q, want %q", got, want)
	}
	if got, want := configs[1].Name, "nl1-example-8443"; got != want {
		t.Errorf("configs[1].Name = %q, want %q", got, want)
	}
	wantRaw := `{"outbounds":[{"protocol":"vmess","settings":{"vnext":[{"address":"de1.example","port":443}]}}],"remarks":"Germany | 1"}`
	if got := string(configs[0].Raw); got != wantRaw {
		t.Errorf("configs[0].Raw = %q, want canonical %q", got, wantRaw)
	}
}

func TestFetchEmptyArray(t *testing.T) {
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "[]")
	}))
	defer panel.Close()

	configs, err := newSource(t, panel.URL).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(configs) != 0 {
		t.Errorf("len(configs) = %d, want 0", len(configs))
	}
}

func TestFetchRejectsNonArray(t *testing.T) {
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"x":1}`)
	}))
	defer panel.Close()

	src := newSource(t, panel.URL)
	configs, err := src.Fetch(context.Background())
	if err == nil {
		t.Fatalf("Fetch = %+v, want an error", configs)
	}
	if !strings.Contains(err.Error(), src.host) {
		t.Errorf("error = %q, want it to name the host %q", err, src.host)
	}
	if strings.Contains(err.Error(), `"x"`) {
		t.Errorf("error = %q, want it to omit the response body", err)
	}
}

func TestFetchHTTPError(t *testing.T) {
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "panel-internal-detail", http.StatusInternalServerError)
	}))
	defer panel.Close()

	src := newSource(t, panel.URL)
	configs, err := src.Fetch(context.Background())
	if err == nil {
		t.Fatalf("Fetch = %+v, want an error", configs)
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error = %q, want it to name the status 500", err)
	}
	if !strings.Contains(err.Error(), src.host) {
		t.Errorf("error = %q, want it to name the host %q", err, src.host)
	}
	if strings.Contains(err.Error(), "panel-internal-detail") {
		t.Errorf("error = %q, want it to omit the response body", err)
	}
}

func TestFetchInvalidJSON(t *testing.T) {
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `[{"outbounds":`)
	}))
	defer panel.Close()

	src := newSource(t, panel.URL)
	if _, err := src.Fetch(context.Background()); err == nil {
		t.Fatal("Fetch: want an error for a truncated array")
	}
}

func TestFetchOversizedBody(t *testing.T) {
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat(" ", 8<<20))
		_, _ = io.WriteString(w, "[]")
	}))
	defer panel.Close()

	src := newSource(t, panel.URL)
	_, err := src.Fetch(context.Background())
	if err == nil {
		t.Fatal("Fetch: want an error for a body over 8 MiB")
	}
	if !strings.Contains(err.Error(), src.host) {
		t.Errorf("error = %q, want it to name the host %q", err, src.host)
	}
}

func TestFetchTimeout(t *testing.T) {
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = io.WriteString(w, "[]")
	}))
	defer panel.Close()

	src := newSource(t, panel.URL)
	if got, want := src.client.Timeout, 20*time.Second; got != want {
		t.Errorf("client timeout = %v, want %v", got, want)
	}
	src.client.Timeout = 20 * time.Millisecond

	if _, err := src.Fetch(context.Background()); err == nil {
		t.Fatal("Fetch: want a timeout error")
	}
}

func TestFetchRejectsCrossOriginRedirect(t *testing.T) {
	var followed atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		followed.Store(true)
		_, _ = io.WriteString(w, "[]")
	}))
	defer other.Close()

	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusFound)
	}))
	defer panel.Close()

	src := newSource(t, panel.URL)
	configs, err := src.Fetch(context.Background())
	if err == nil {
		t.Fatalf("Fetch = %+v, want a cross-origin redirect error", configs)
	}
	if followed.Load() {
		t.Error("the redirect target was called, want the redirect rejected")
	}
	if !strings.Contains(err.Error(), "redirect") {
		t.Errorf("error = %q, want it to mention the rejected redirect", err)
	}
}

func TestTransportIgnoresProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://proxy.example:3128")
	t.Setenv("HTTPS_PROXY", "http://proxy.example:3128")

	src := newSource(t, "https://panel.example")
	transport, ok := src.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport is %T, want *http.Transport", src.client.Transport)
	}
	if transport.Proxy != nil {
		t.Error("Transport.Proxy is set, want nil so the panel is never reached through a proxy")
	}
}

func TestFetchErrorsRedactSubscription(t *testing.T) {
	const secret = "short-secret"
	const subscriptionPath = "/api/sub/" + secret

	assertRedacted := func(t *testing.T, err error) {
		t.Helper()
		if err == nil {
			t.Fatal("Fetch: want an error")
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error = %q, want it to omit the shortUuid", err)
		}
		if strings.Contains(err.Error(), "/api/sub") {
			t.Errorf("error = %q, want it to omit the URL path", err)
		}
	}

	t.Run("status", func(t *testing.T) {
		panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "panel-internal-detail", http.StatusBadGateway)
		}))
		defer panel.Close()

		_, err := newSource(t, panel.URL+subscriptionPath).Fetch(context.Background())
		assertRedacted(t, err)
	})

	t.Run("transport", func(t *testing.T) {
		panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		panel.Close()

		_, err := newSource(t, panel.URL+subscriptionPath).Fetch(context.Background())
		assertRedacted(t, err)
	})

	t.Run("redirect", func(t *testing.T) {
		other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "[]")
		}))
		defer other.Close()

		panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, other.URL+r.URL.Path, http.StatusFound)
		}))
		defer panel.Close()

		_, err := newSource(t, panel.URL+subscriptionPath).Fetch(context.Background())
		assertRedacted(t, err)
	})
}

func TestFetchNonObjectEntryIsAConfig(t *testing.T) {
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `["x"]`)
	}))
	defer panel.Close()

	configs, err := newSource(t, panel.URL).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(configs) != 1 {
		t.Fatalf("len(configs) = %d, want 1", len(configs))
	}
	if got, want := configs[0].Name, "config-1"; got != want {
		t.Errorf("configs[0].Name = %q, want %q", got, want)
	}
	if got, want := string(configs[0].Raw), `"x"`; got != want {
		t.Errorf("configs[0].Raw = %q, want %q", got, want)
	}
}
