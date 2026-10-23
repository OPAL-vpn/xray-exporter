package logging

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"sync"
)

// RotatingWriter appends to one file and trims its oldest bytes to keep it
// bounded, so one path holds a contiguous run of recent records.
type RotatingWriter struct {
	path    string
	max     int64
	keep    int64
	retryAt int64
	f       *os.File
	size    int64
	log     *slog.Logger
	warned  bool
	mu      sync.Mutex
}

// OpenRotating opens path for appending, trimming it once if it already exceeds
// max. max == 0 disables trimming; keep is the byte count a trim retains. logger
// receives trim failures and may be nil.
func OpenRotating(path string, max, keep int64, logger *slog.Logger) (*RotatingWriter, error) {
	// O_RDWR because a trim reads the tail from the same descriptor it appends to.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0o640)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	w := &RotatingWriter{path: path, max: max, keep: keep, f: f, size: info.Size(), log: logger}
	if max > 0 && w.size > max {
		w.trim()
	}
	return w, nil
}

// Write appends p, trimming once the file passes max. A failed trim leaves the
// record appended and is retried after another window of growth.
func (w *RotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	n, err := w.f.Write(p)
	w.size += int64(n)
	if err == nil && w.max > 0 && w.size > w.max && w.size >= w.retryAt {
		w.trimLocked()
	}
	return n, err
}

// Close closes the file.
func (w *RotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Close()
}

func (w *RotatingWriter) trim() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.trimLocked()
}

// trimLocked replaces the file with its newest keep bytes. The lock is held
// across the copy, so a record logged mid-trim waits for the rename rather than
// landing in the file being replaced.
func (w *RotatingWriter) trimLocked() {
	// A host copytruncate or a failed earlier rename moves the real size away
	// from the counter; fstat is cheap once per window.
	if info, err := w.f.Stat(); err == nil {
		w.size = info.Size()
	}
	if w.size <= w.keep {
		return
	}

	start, err := w.tailStart()
	if err != nil {
		w.trimFailed(err)
		return
	}

	tmpPath := w.path + ".trim"
	tmp, err := os.OpenFile(tmpPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o640)
	if err != nil {
		w.trimFailed(err)
		return
	}
	if _, err = io.Copy(tmp, io.NewSectionReader(w.f, start, w.size-start)); err == nil {
		err = tmp.Chmod(0o640)
	}
	if err == nil {
		err = os.Rename(tmpPath, w.path)
	}
	if err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		w.trimFailed(err)
		return
	}

	// The renamed descriptor already sits at EOF, and following it instead of
	// reopening the path keeps appending to the file we just installed even if
	// something else moved the path in between.
	w.f.Close()
	w.f = tmp
	w.size -= start
	w.retryAt = 0
	w.warned = false
}

// tailStart returns the offset of the first record boundary at or after the kept
// window, or the file size when the window holds no whole record.
func (w *RotatingWriter) tailStart() (int64, error) {
	off := w.size - w.keep
	if off <= 0 {
		return 0, nil
	}

	var prev [1]byte
	if _, err := w.f.ReadAt(prev[:], off-1); err != nil {
		return 0, err
	}
	if prev[0] == '\n' {
		return off, nil
	}

	buf := make([]byte, 64<<10)
	for pos := off; pos < w.size; {
		n, err := w.f.ReadAt(buf, pos)
		if i := bytes.IndexByte(buf[:n], '\n'); i >= 0 {
			return pos + int64(i) + 1, nil
		}
		if n == 0 || err != nil {
			break
		}
		pos += int64(n)
	}
	return w.size, nil
}

// trimFailed reports the first failure only: a full disk fails every crossing,
// and one warning per log record would bury the reasons that matter.
func (w *RotatingWriter) trimFailed(err error) {
	if w.log != nil && !w.warned {
		w.log.Warn("trimming log file", "path", w.path, "error", err)
	}
	w.warned = true
	backoff := w.max - w.keep
	if backoff <= 0 {
		backoff = w.keep
	}
	w.retryAt = w.size + backoff
}
