package fetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func payload(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i * 7)
	}
	return b
}

func digest(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// server serves body; honours Range unless noRange; counts requests and
// remembers the last Range header.
type server struct {
	*httptest.Server
	hits      atomic.Int32
	lastRange atomic.Value
}

func newServer(body []byte, noRange bool) *server {
	s := &server{}
	s.lastRange.Store("")
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		rg := r.Header.Get("Range")
		s.lastRange.Store(rg)
		if rg == "" || noRange {
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			w.Write(body)
			return
		}
		var from int64
		fmt.Sscanf(rg, "bytes=%d-", &from)
		if from >= int64(len(body)) {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", len(body)))
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, len(body)-1, len(body)))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(body[from:])
	}))
	return s
}

func TestDownloadVerifiesAndReportsProgress(t *testing.T) {
	body := payload(1 << 20)
	s := newServer(body, false)
	defer s.Close()
	dest := filepath.Join(t.TempDir(), "sub", "m.gguf")
	var last, total int64
	got, err := Download(context.Background(), s.URL, dest, Options{SHA256: "sha256:" + strings.ToUpper(digest(body)),
		Progress: func(d, t int64) { last, total = d, t }})
	if err != nil || got != digest(body) {
		t.Fatalf("%v %s", err, got)
	}
	if last != int64(len(body)) || total != int64(len(body)) {
		t.Fatalf("progress %d/%d", last, total)
	}
	if b, _ := os.ReadFile(dest); len(b) != len(body) {
		t.Fatal("not in place")
	}
	if _, err := os.Stat(dest + ".part"); err == nil {
		t.Fatal(".part left behind")
	}
	// Already there and verified: no request at all.
	hits := s.hits.Load()
	if _, err := Download(context.Background(), s.URL, dest, Options{SHA256: digest(body)}); err != nil || s.hits.Load() != hits {
		t.Fatalf("redownloaded: %v", err)
	}
}

func TestResumeUsesRange(t *testing.T) {
	body := payload(300_000)
	s := newServer(body, false)
	defer s.Close()
	dest := filepath.Join(t.TempDir(), "m.gguf")
	os.WriteFile(dest+".part", body[:120_000], 0o644)
	got, err := Download(context.Background(), s.URL, dest, Options{SHA256: digest(body)})
	if err != nil || got != digest(body) {
		t.Fatalf("%v", err)
	}
	if s.lastRange.Load() != "bytes=120000-" {
		t.Fatalf("range %q", s.lastRange.Load())
	}
}

func TestServerIgnoringRangeRestarts(t *testing.T) {
	body := payload(100_000)
	s := newServer(body, true)
	defer s.Close()
	dest := filepath.Join(t.TempDir(), "m.gguf")
	os.WriteFile(dest+".part", body[:40_000], 0o644)
	if _, err := Download(context.Background(), s.URL, dest, Options{SHA256: digest(body)}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dest); len(b) != len(body) {
		t.Fatalf("size %d", len(b))
	}
}

func TestCompletePartIsVerifiedWithoutRedownload(t *testing.T) {
	body := payload(50_000)
	s := newServer(body, false)
	defer s.Close()
	dest := filepath.Join(t.TempDir(), "m.gguf")
	os.WriteFile(dest+".part", body, 0o644) // an interrupted rename: all bytes there
	if _, err := Download(context.Background(), s.URL, dest, Options{SHA256: digest(body)}); err != nil {
		t.Fatal(err)
	}
}

func TestStalePartLargerThanFileRestarts(t *testing.T) {
	body := payload(10_000)
	s := newServer(body, false)
	defer s.Close()
	dest := filepath.Join(t.TempDir(), "m.gguf")
	os.WriteFile(dest+".part", payload(20_000), 0o644)
	if _, err := Download(context.Background(), s.URL, dest, Options{SHA256: digest(body)}); err != nil {
		t.Fatal(err)
	}
}

func TestMismatchKeepsBad(t *testing.T) {
	body := payload(10_000)
	s := newServer(body, false)
	defer s.Close()
	dest := filepath.Join(t.TempDir(), "m.gguf")
	_, err := Download(context.Background(), s.URL, dest, Options{SHA256: digest([]byte("other"))})
	if !errors.Is(err, ErrMismatch) {
		t.Fatalf("%v", err)
	}
	if _, err := os.Stat(dest + ".bad"); err != nil {
		t.Fatal("no .bad kept")
	}
	if _, err := os.Stat(dest); err == nil {
		t.Fatal("bad file in place")
	}
	if _, err := os.Stat(dest + ".part"); err == nil {
		t.Fatal(".part left")
	}
}

func TestNeedsADigestOrUnverified(t *testing.T) {
	body := payload(1000)
	s := newServer(body, false)
	defer s.Close()
	dest := filepath.Join(t.TempDir(), "x")
	if _, err := Download(context.Background(), s.URL, dest, Options{}); !errors.Is(err, ErrNoDigest) {
		t.Fatalf("%v", err)
	}
	got, err := Download(context.Background(), s.URL, dest, Options{Unverified: true})
	if err != nil || got != digest(body) {
		t.Fatalf("%v", err)
	}
}

func TestTruncatedBodyKeepsPartForResume(t *testing.T) {
	body := payload(100_000)
	var cut atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "" && !cut.Swap(true) {
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			w.Write(body[:30_000]) // then the connection ends short
			return
		}
		var from int64
		fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-", &from)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, len(body)-1, len(body)))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(body[from:])
	}))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "m")
	if _, err := Download(context.Background(), srv.URL, dest, Options{SHA256: digest(body)}); err == nil {
		t.Fatal("short body accepted")
	}
	fi, err := os.Stat(dest + ".part")
	if err != nil || fi.Size() != 30_000 {
		t.Fatalf("part: %v", err)
	}
	if _, err := Download(context.Background(), srv.URL, dest, Options{SHA256: digest(body)}); err != nil {
		t.Fatalf("resume: %v", err)
	}
}

func TestFindHashesOnlySizeMatches(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	want := payload(5000)
	os.MkdirAll(filepath.Join(b, "models", "x"), 0o755)
	os.WriteFile(filepath.Join(a, "other.gguf"), payload(4000), 0o644)
	os.WriteFile(filepath.Join(a, "decoy.gguf"), append([]byte{1}, want[1:]...), 0o644) // same size, other bytes
	os.WriteFile(filepath.Join(b, "models", "x", "renamed.bin"), want, 0o644)
	os.WriteFile(filepath.Join(b, "partial.gguf.part"), want, 0o644)
	got, ok := Find("sha256:"+digest(want), int64(len(want)), a, b)
	if !ok || filepath.Base(got) != "renamed.bin" {
		t.Fatalf("%q %v", got, ok)
	}
	if _, ok := Find(digest(want), int64(len(want))+1, a, b); ok {
		t.Fatal("found at the wrong size")
	}
	if _, ok := Find(digest(want), int64(len(want)), filepath.Join(a, "missing")); ok {
		t.Fatal("found in a missing root")
	}
	if _, ok := Find("", 10, a); ok {
		t.Fatal("empty digest")
	}
}
