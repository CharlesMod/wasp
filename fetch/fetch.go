// Package fetch downloads files a person can trust: resumable (.part and
// Range), checked against a sha256 on arrival, and found again on disk by
// digest before anything is downloaded twice.
package fetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ErrMismatch is a download whose digest is not the one promised. The file is
// kept beside the destination as .bad and the destination is not written.
var ErrMismatch = errors.New("fetch: sha256 mismatch")

// ErrNoDigest is a download asked for without a digest or Unverified.
var ErrNoDigest = errors.New("fetch: no sha256 to verify against (set Unverified to download without one)")

// Progress is told how many bytes of how many are in place (total is -1 when
// the server does not say). It is called as bytes arrive.
type Progress func(done, total int64)

// Options shapes one download.
type Options struct {
	SHA256     string // expected digest, hex, with or without a "sha256:" prefix
	Unverified bool   // download with no expected digest; the computed one is returned
	Client     *http.Client
	Progress   Progress
	Header     http.Header // extra request headers (a token)
}

// Digest normalises "sha256:ABC…" to lower-case hex.
func Digest(s string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), "sha256:"))
}

// Download fetches url to dest and returns the file's sha256 (hex). It resumes
// dest+".part" with a Range request when one is there, restarts if the server
// ignores Range, and verifies the whole file before renaming it into place. A
// dest that already verifies is left alone.
func Download(ctx context.Context, url, dest string, o Options) (string, error) {
	want := Digest(o.SHA256)
	if want == "" && !o.Unverified {
		return "", ErrNoDigest
	}
	if want != "" && fileDigest(dest) == want {
		return want, nil
	}
	c := o.Client
	if c == nil {
		c = http.DefaultClient
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	part := dest + ".part"
	for attempt := 0; attempt < 2; attempt++ {
		off := int64(0)
		if fi, err := os.Stat(part); err == nil {
			off = fi.Size()
		}
		_, err := fill(ctx, c, url, part, off, o)
		if errors.Is(err, errRestart) && attempt == 0 {
			_ = os.Remove(part)
			continue
		}
		if err != nil {
			return "", err
		}
		break
	}
	got, err := Hash(part)
	if err != nil {
		return "", err
	}
	if want != "" && got != want {
		bad := dest + ".bad"
		_ = os.Remove(bad)
		_ = os.Rename(part, bad)
		return got, fmt.Errorf("%w: got %s, want %s (kept as %s)", ErrMismatch, got, want, filepath.Base(bad))
	}
	if err := os.Rename(part, dest); err != nil {
		return got, err
	}
	return got, nil
}

var errRestart = errors.New("restart")

// fill appends to part from offset off. It returns errRestart when part
// cannot be continued (a stale or oversized .part).
func fill(ctx context.Context, c *http.Client, url, part string, off int64, o Options) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	for k, vs := range o.Header {
		req.Header[k] = vs
	}
	if off > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", off))
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	flags := os.O_CREATE | os.O_WRONLY
	total := int64(-1)
	switch resp.StatusCode {
	case http.StatusPartialContent:
		if cr := resp.Header.Get("Content-Range"); cr != "" {
			if i := strings.LastIndex(cr, "/"); i >= 0 {
				if n, err := strconv.ParseInt(cr[i+1:], 10, 64); err == nil {
					total = n
				}
			}
		}
		flags |= os.O_APPEND
	case http.StatusOK:
		// The server ignored Range (or there was nothing to resume): start over.
		off = 0
		flags |= os.O_TRUNC
		if resp.ContentLength >= 0 {
			total = resp.ContentLength
		}
	case http.StatusRequestedRangeNotSatisfiable:
		// The .part is already the whole file, or larger than the file now is.
		if cr := resp.Header.Get("Content-Range"); strings.HasPrefix(cr, "bytes */") {
			if n, err := strconv.ParseInt(strings.TrimPrefix(cr, "bytes */"), 10, 64); err == nil && n == off {
				return off, nil
			}
		}
		return 0, errRestart
	default:
		return 0, fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	f, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return 0, err
	}
	done := off
	buf := make([]byte, 256<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				return done, werr
			}
			done += int64(n)
			if o.Progress != nil {
				o.Progress(done, total)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			return done, rerr // the .part stays for the next try
		}
	}
	if err := f.Close(); err != nil {
		return done, err
	}
	if total >= 0 && done != total {
		return done, fmt.Errorf("fetch: %s ended at %d of %d bytes: %w", url, done, total, io.ErrUnexpectedEOF)
	}
	return done, nil
}

// Hash is the sha256 (hex) of a file.
func Hash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fileDigest(path string) string {
	if fi, err := os.Stat(path); err != nil || fi.IsDir() {
		return ""
	}
	d, _ := Hash(path)
	return d
}

// Find looks for a file with this sha256 under the roots, so a model another
// product (or an earlier run) already downloaded is not downloaded again.
// Only regular files whose size is exactly size are hashed (size must be
// known: it is what keeps the search from hashing every file on the disk).
// .part and .bad files are skipped. It returns the path, or false.
func Find(sha256hex string, size int64, roots ...string) (string, bool) {
	want := Digest(sha256hex)
	if want == "" || size <= 0 {
		return "", false
	}
	for _, root := range roots {
		var found string
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil || found != "" {
				if err != nil && d != nil && d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() || strings.HasSuffix(p, ".part") || strings.HasSuffix(p, ".bad") {
				return nil
			}
			fi, err := d.Info()
			if err != nil || !fi.Mode().IsRegular() || fi.Size() != size {
				return nil
			}
			if h, err := Hash(p); err == nil && h == want {
				found = p
			}
			return nil
		})
		if found != "" {
			return found, true
		}
	}
	return "", false
}
