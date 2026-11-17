package probe

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

// DownloadBytes GETs url through d until the body ends or ctx is done,
// discarding what it reads, and returns the bytes moved. Non-2xx is an error
// and returns 0 bytes: an error body is not load, so a redirect or an error
// page must not make a loaded sample look real. A transfer error keeps the
// bytes moved so far, because a transfer cancelled mid-body is real load. The
// context alone bounds the transfer: the file size belongs to the operator, so
// there is no client timeout.
func DownloadBytes(ctx context.Context, d Dialer, url string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, fmt.Errorf("download %s: %w", url, err)
	}

	resp, err := HTTPClient(d, 0, nil).Do(req)
	if err != nil {
		return 0, fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()

	n, copyErr := io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return 0, fmt.Errorf("download %s: status %d", url, resp.StatusCode)
	}
	if copyErr != nil {
		return n, fmt.Errorf("download %s: %w", url, copyErr)
	}

	return n, nil
}
