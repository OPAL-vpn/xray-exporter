package vantage

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func serve(status int, body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func TestLookup(t *testing.T) {
	// A documentation-range address; the shape is that of the real answer.
	srv := serve(200, `{"ip":"203.0.113.9","city":"Moscow","country":"RU","org":"AS12714 PJSC MegaFon"}`)
	defer srv.Close()

	got, err := newDetector(srv.URL).Lookup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := (Identity{ASName: "PJSC MegaFon", Country: "RU"}); got != want {
		t.Errorf("Lookup = %+v, want %+v", got, want)
	}
}

func TestLookupFailures(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
	}{
		"status":   {503, "down"},
		"no org":   {200, `{"ip":"203.0.113.9","country":"RU"}`},
		"no AS":    {200, `{"org":"PJSC MegaFon"}`},
		"not JSON": {200, "<html>"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			srv := serve(c.status, c.body)
			defer srv.Close()
			if _, err := newDetector(srv.URL).Lookup(context.Background()); err == nil {
				t.Error("accepted")
			}
		})
	}
}

func TestLookupIgnoresRedirects(t *testing.T) {
	target := serve(200, `{"org":"AS1 X","country":"RU"}`)
	defer target.Close()
	redirect := httptest.NewServer(http.RedirectHandler(target.URL, http.StatusFound))
	defer redirect.Close()

	if _, err := newDetector(redirect.URL).Lookup(context.Background()); err == nil {
		t.Error("a redirected answer was accepted")
	}
}

func TestLookupContextCancelled(t *testing.T) {
	srv := serve(200, `{"org":"AS1 X"}`)
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := newDetector(srv.URL).Lookup(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}
