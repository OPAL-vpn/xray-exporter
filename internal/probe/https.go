package probe

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"time"
)

// UserAgent is the fixed user agent every request the exporter sends, so a
// probe looks the same from every vantage point.
const UserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36"

// HTTPResult is the outcome of one HTTPS probe.
type HTTPResult struct {
	// Up is true when a response arrived, whatever its status.
	Up bool
	// Status is the response status code, 0 when no response arrived.
	Status int
	// TLSVerified is true when the server certificate chained to a trusted
	// root. A certificate failure is recorded here, never returned as Err:
	// tls_ok=0 with a real status is itself a signal.
	TLSVerified bool
	// TTFB is the time to the first response byte.
	TTFB time.Duration
	// Total is the time until the response body was fully read.
	Total time.Duration
	// Err is the transport error, if the request never produced a response.
	Err error
}

// HTTPClient returns the HTTP client the package shares: connections go
// through d, keep-alives are off, redirects come back as responses, and the
// leaf is verified against the host of the requested URL, whether that is a
// DNS name or an address literal (VerifyHostname matches IP SANs). roots
// overrides the system roots when non-nil.
//
// The transport derives that name from the URL it is dialing, so verification
// does not depend on the connection state: Go sends no SNI for an address
// literal, and the state carries no other copy of the name. ProbeHTTPS
// replaces the callback to keep the handshake and record a verdict instead of
// failing the request.
func HTTPClient(d Dialer, timeout time.Duration, roots *x509.CertPool) *http.Client {
	transport := &http.Transport{
		DialContext:       d.DialContext,
		DisableKeepAlives: true,
		TLSClientConfig:   &tls.Config{RootCAs: roots},
	}

	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// ProbeHTTPS issues GET https://<host>/ through d and reports what came back.
// Redirects are not followed, keep-alives are off, and certificate verification
// is recorded in TLSVerified instead of failing the request. roots overrides
// the system roots when non-nil. timeout bounds the whole exchange.
func ProbeHTTPS(ctx context.Context, d Dialer, host string, timeout time.Duration, roots *x509.CertPool) HTTPResult {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	target, err := probeURL(host)
	if err != nil {
		return HTTPResult{Err: err}
	}

	var (
		start      time.Time
		ttfb       time.Duration
		tlsChecked bool
		verifyErr  error
	)

	client := HTTPClient(d, timeout, roots)
	transport := client.Transport.(*http.Transport)
	defer client.CloseIdleConnections()

	// A probe needs the certificate error as a verdict, not as a failed
	// request: skip the standard verification, record our own, and let the
	// handshake continue. The name comes from the request URL, not from the
	// connection state, which has no SNI name for an address literal.
	transport.TLSClientConfig.InsecureSkipVerify = true
	transport.TLSClientConfig.VerifyConnection = func(state tls.ConnectionState) error {
		tlsChecked = true
		verifyErr = verifyPeer(state, roots, target.Hostname())
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return HTTPResult{Err: err}
	}
	req.Header.Set("User-Agent", UserAgent)

	trace := &httptrace.ClientTrace{
		GotFirstResponseByte: func() { ttfb = time.Since(start) },
	}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))

	start = time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return HTTPResult{Err: err, TTFB: ttfb, Total: time.Since(start)}
	}
	defer resp.Body.Close()

	// Reading the body to the end is what makes Total the full request time;
	// the request context bounds a body that never finishes.
	_, _ = io.Copy(io.Discard, resp.Body)

	return HTTPResult{
		Up:          true,
		Status:      resp.StatusCode,
		TLSVerified: tlsChecked && verifyErr == nil,
		TTFB:        ttfb,
		Total:       time.Since(start),
	}
}

// probeURL turns a target host into the request URL, adding the scheme and the
// root path when the caller did not supply them.
func probeURL(host string) (*url.URL, error) {
	raw := strings.TrimSpace(host)
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}

	target, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if target.Host == "" {
		return nil, errors.New("probe: empty host")
	}
	if target.Path == "" {
		target.Path = "/"
	}

	return target, nil
}

// verifyPeer checks the peer chain against roots and then the leaf against
// name, which may be a DNS name or an address literal. Both checks matter: a
// chain that reaches a trusted root says nothing about who the certificate was
// issued to.
func verifyPeer(state tls.ConnectionState, roots *x509.CertPool, name string) error {
	if len(state.PeerCertificates) == 0 {
		return errors.New("no peer certificate")
	}

	leaf := state.PeerCertificates[0]
	intermediates := x509.NewCertPool()
	for _, cert := range state.PeerCertificates[1:] {
		intermediates.AddCert(cert)
	}

	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: intermediates,
	}); err != nil {
		return err
	}
	if name == "" {
		return errors.New("no name to verify the certificate against")
	}

	return leaf.VerifyHostname(name)
}
