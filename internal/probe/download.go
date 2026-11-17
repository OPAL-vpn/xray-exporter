package probe

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

// DownloadBytes GETs url through d until the body ends or ctx is done,
// discarding what it reads, and returns the bytes moved. Non-2xx is an
// error; bytes read from an error body still count, because the caller judges
// a loaded sample by whether traffic moved at all. The context alone bounds the
// transfer: the file size belongs to the operator, so there is no client
// timeout.
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
		return n, fmt.Errorf("download %s: status %d", url, resp.StatusCode)
	}
	if copyErr != nil {
		return n, fmt.Errorf("download %s: %w", url, copyErr)
	}

	return n, nil
}
