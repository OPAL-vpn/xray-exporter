package probe

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptrace"
	"time"
)

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
