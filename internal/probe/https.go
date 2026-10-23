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
	Up     bool
	Status int
	// TLSVerified records a certificate failure as a verdict instead of an Err:
	// tls_ok=0 with a real status is itself a signal.
	TLSVerified bool
	TTFB        time.Duration
	Total       time.Duration
	Err         error
}

// ProbeHTTPS issues GET https://<host>/ through d, recording the certificate
// verdict instead of failing the request.
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

	// A certificate error is a verdict, not a failed request: verify it ourselves
	// and let the handshake continue. The name comes from the URL because an
	// address literal sends no SNI.
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

	// Read the body to the end so Total covers the full request.
	_, _ = io.Copy(io.Discard, resp.Body)

	return HTTPResult{
		Up:          true,
		Status:      resp.StatusCode,
		TLSVerified: tlsChecked && verifyErr == nil,
		TTFB:        ttfb,
		Total:       time.Since(start),
	}
}
