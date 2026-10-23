package logging_test

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/OPAL-vpn/xray-exporter/internal/logging"
)

// rec renders one 40-byte record, so a kept window can be aimed at a boundary
// or across one on purpose.
func rec(i int) string {
	return fmt.Sprintf("rec-%04d %s\n", i, strings.Repeat("x", 30))
}

func openWriter(t *testing.T, max, keep int64) (*logging.RotatingWriter, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "xray.log")
	w, err := logging.OpenRotating(path, max, keep, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("OpenRotating: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w, path
}

func appendRecords(t *testing.T, w io.Writer, from, to int) {
	t.Helper()
	for i := from; i < to; i++ {
		if _, err := io.WriteString(w, rec(i)); err != nil {
			t.Fatalf("write rec %d: %v", i, err)
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Size()
}

func TestRotatingWriterTrimsToNewestRecords(t *testing.T) {
	w, path := openWriter(t, 200, 120)
	appendRecords(t, w, 0, 10)

	if got := fileSize(t, path); got > 200 {
		t.Fatalf("size = %d, want <= 200", got)
	}
	body := readFile(t, path)
	if !strings.HasSuffix(body, rec(9)) {
		t.Errorf("newest record missing: %q", body)
	}
	if !strings.HasPrefix(body, "rec-") {
		t.Errorf("file starts mid-record: %q", body)
	}
	if strings.Contains(body, rec(0)) {
		t.Errorf("oldest record survived the trim: %q", body)
	}
}

func TestRotatingWriterKeepsAppendingAfterTrim(t *testing.T) {
	w, path := openWriter(t, 200, 120)
	appendRecords(t, w, 0, 6) // crosses max, trims

	appendRecords(t, w, 6, 7)
	body := readFile(t, path)
	if !strings.HasSuffix(body, rec(6)) {
		t.Fatalf("file does not end with the record written after the trim: %q", body)
	}
	if !strings.HasPrefix(body, "rec-") {
		t.Errorf("file starts mid-record after appending: %q", body)
	}
}

func TestRotatingWriterTrimsOversizedFileAtOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "xray.log")
	var seeded strings.Builder
	for i := range 10 {
		seeded.WriteString(rec(i))
	}
	if err := os.WriteFile(path, []byte(seeded.String()), 0o640); err != nil {
		t.Fatalf("seed: %v", err)
	}

	w, err := logging.OpenRotating(path, 200, 120, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("OpenRotating: %v", err)
	}
	defer w.Close()

	if got := fileSize(t, path); got > 200 {
		t.Fatalf("size = %d, want <= 200 after open", got)
	}
	if body := readFile(t, path); !strings.HasSuffix(body, rec(9)) {
		t.Errorf("newest record missing after open trim: %q", body)
	}
}

func TestRotatingWriterLeavesSmallFileAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "xray.log")
	var seeded strings.Builder
	for i := range 4 {
		seeded.WriteString(rec(i))
	}
	if err := os.WriteFile(path, []byte(seeded.String()), 0o640); err != nil {
		t.Fatalf("seed: %v", err)
	}

	w, err := logging.OpenRotating(path, 200, 120, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("OpenRotating: %v", err)
	}
	defer w.Close()

	if body := readFile(t, path); body != seeded.String() {
		t.Errorf("small file rewritten: %q", body)
	}
	if _, err := os.Stat(path + ".trim"); !os.IsNotExist(err) {
		t.Errorf("stat .trim = %v, want it never created for a small file", err)
	}
}

func TestRotatingWriterDisablesTrimWhenMaxIsZero(t *testing.T) {
	w, path := openWriter(t, 0, 0)
	appendRecords(t, w, 0, 10)

	if got := fileSize(t, path); got != 400 {
		t.Fatalf("size = %d, want all 400 bytes kept with max=0", got)
	}
}

func TestRotatingWriterDropsPartialLeadingRecord(t *testing.T) {
	// max-keep = 130, so the window opens 10 bytes into record 3.
	w, path := openWriter(t, 200, 110)
	appendRecords(t, w, 0, 6)

	body := readFile(t, path)
	for i, line := range strings.Split(strings.TrimSuffix(body, "\n"), "\n") {
		if len(line) != len(strings.TrimSuffix(rec(0), "\n")) || !strings.HasPrefix(line, "rec-") {
			t.Fatalf("line %d is not a whole record: %q", i, line)
		}
	}
	if !strings.HasPrefix(body, rec(4)) {
		t.Errorf("file starts at %q, want the first whole record at or after the window", body[:min(len(body), 40)])
	}
}

func TestRotatingWriterDropsEverythingWhenKeepFitsNoRecord(t *testing.T) {
	w, path := openWriter(t, 200, 100)
	long := strings.Repeat("y", 300) + "\n"
	if _, err := io.WriteString(w, long); err != nil {
		t.Fatalf("write long record: %v", err)
	}
	if body := readFile(t, path); body != "" {
		t.Fatalf("file = %q, want empty: the kept window holds no whole record", body)
	}

	appendRecords(t, w, 0, 1)
	if body := readFile(t, path); body != rec(0) {
		t.Errorf("file = %q, want the record written after the trim", body)
	}
}

func TestRotatingWriterSurvivesTrimFailure(t *testing.T) {
	var logs bytes.Buffer
	path := filepath.Join(t.TempDir(), "xray.log")
	// A directory in the trim file's place makes the replacement impossible.
	if err := os.Mkdir(path+".trim", 0o755); err != nil {
		t.Fatalf("mkdir .trim: %v", err)
	}

	w, err := logging.OpenRotating(path, 200, 120, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatalf("OpenRotating: %v", err)
	}
	defer w.Close()

	appendRecords(t, w, 0, 6)

	body := readFile(t, path)
	if len(body) != 240 {
		t.Errorf("file is %d bytes, want all 240 kept after a failed trim", len(body))
	}
	for i := range 6 {
		if !strings.Contains(body, rec(i)) {
			t.Errorf("record %d lost by the failed trim", i)
		}
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "trimming log file") {
		t.Errorf("no warning logged: %q", logs.String())
	}
}

func TestRotatingWriterOverwritesStaleTrimFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "xray.log")
	if err := os.WriteFile(path+".trim", []byte("left over from a killed process"), 0o640); err != nil {
		t.Fatalf("seed .trim: %v", err)
	}

	w, err := logging.OpenRotating(path, 200, 120, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("OpenRotating: %v", err)
	}
	defer w.Close()
	appendRecords(t, w, 0, 6)

	if body := readFile(t, path); !strings.HasPrefix(body, "rec-") || strings.Contains(body, "left over") {
		t.Errorf("stale trim file leaked into the log: %q", body)
	}
	if _, err := os.Stat(path + ".trim"); !os.IsNotExist(err) {
		t.Errorf("stat .trim = %v, want it renamed away", err)
	}
}

func TestRotatingWriterConcurrentWritersStayOnRecordBoundaries(t *testing.T) {
	w, path := openWriter(t, 800, 400)

	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 200 {
				if _, err := io.WriteString(w, rec(g*1000+i)); err != nil {
					t.Errorf("write: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	if got := fileSize(t, path); got > 800 {
		t.Errorf("size = %d, want <= 800 after concurrent trims", got)
	}
	body := readFile(t, path)
	if body == "" {
		t.Fatal("file empty after 1600 concurrent records")
	}
	for i, line := range strings.Split(strings.TrimSuffix(body, "\n"), "\n") {
		if len(line) != len(strings.TrimSuffix(rec(0), "\n")) || !strings.HasPrefix(line, "rec-") {
			t.Fatalf("line %d is torn: %q", i, line)
		}
	}
}

func TestRotatingWriterWarnsOnceWhileTrimKeepsFailing(t *testing.T) {
	var logs bytes.Buffer
	path := filepath.Join(t.TempDir(), "xray.log")
	if err := os.Mkdir(path+".trim", 0o755); err != nil {
		t.Fatalf("mkdir .trim: %v", err)
	}

	w, err := logging.OpenRotating(path, 200, 120, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatalf("OpenRotating: %v", err)
	}
	defer w.Close()

	appendRecords(t, w, 0, 40) // many crossings, every trim fails
	if got := strings.Count(logs.String(), "trimming log file"); got != 1 {
		t.Errorf("logged %d trim warnings over 40 records, want 1:\n%s", got, logs.String())
	}
	if body := readFile(t, path); !strings.Contains(body, rec(39)) {
		t.Errorf("appends stopped after failed trims: %q", body[len(body)-80:])
	}
}

func TestRotatingWriterRecoversAfterExternalTruncate(t *testing.T) {
	w, path := openWriter(t, 200, 120)
	appendRecords(t, w, 0, 4)

	if err := os.Truncate(path, 0); err != nil { // a host logrotate copytruncate
		t.Fatalf("truncate: %v", err)
	}
	appendRecords(t, w, 4, 20)

	body := readFile(t, path)
	if !strings.HasSuffix(body, rec(19)) {
		t.Errorf("file does not end with the newest record: %q", body)
	}
	if strings.HasPrefix(body, "\x00") || strings.Contains(body, strings.Repeat("\x00", 4)) {
		t.Errorf("file was not rewritten from the truncation point")
	}
}
