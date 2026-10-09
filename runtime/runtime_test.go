package runtime

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fakeArchive(t *testing.T, goos string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	name := "python/bin/python3"
	if goos == "windows" {
		name = "python/python.exe"
	}
	body := []byte("#!/bin/sh\necho fake\n")
	tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
	tw.Write(body)
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func server(t *testing.T, goos, goarch string, corrupt bool) (*httptest.Server, *int32) {
	triple, _ := Triple(goos, goarch)
	asset := AssetName(DefaultVersion, "T1", triple)
	data := fakeArchive(t, goos)
	sum := sha256.Sum256(data)
	if corrupt {
		sum[0] ^= 0xff
	}
	var hits int32
	mux := http.NewServeMux()
	mux.HandleFunc("/T1/SHA256SUMS", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%x  %s\n%x  other.tar.gz\n", sum, asset, sum)
	})
	mux.HandleFunc("/T1/"+asset, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.ServeContent(w, r, asset, time.Time{}, bytes.NewReader(data))
	})
	return httptest.NewServer(mux), &hits
}

func TestPythonDownloadsVerifiesAndReuses(t *testing.T) {
	for _, c := range [][2]string{{"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "arm64"}, {"windows", "amd64"}} {
		t.Run(c[0]+"-"+c[1], func(t *testing.T) {
			srv, hits := server(t, c[0], c[1], false)
			defer srv.Close()
			root := t.TempDir()
			o := Options{BaseURL: srv.URL, Tag: "T1", GOOS: c[0], GOARCH: c[1]}
			exe, err := Python(context.Background(), root, "", o)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(exe); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(exe, root) {
				t.Fatalf("interpreter outside the root: %s", exe)
			}
			if _, err := Python(context.Background(), root, "", o); err != nil {
				t.Fatal(err)
			}
			if *hits != 1 {
				t.Fatalf("downloaded %d times, want 1", *hits)
			}
		})
	}
}

func TestPythonRefusesBadDigest(t *testing.T) {
	srv, _ := server(t, "linux", "amd64", true)
	defer srv.Close()
	root := t.TempDir()
	_, err := Python(context.Background(), root, "", Options{BaseURL: srv.URL, Tag: "T1", GOOS: "linux", GOARCH: "amd64"})
	if err == nil {
		t.Fatal("a digest mismatch must fail")
	}
	if _, serr := os.Stat(Interpreter(Dir(root, DefaultVersion, "T1"), "linux")); serr == nil {
		t.Fatal("nothing may be laid after a mismatch")
	}
}

func TestUnsupportedPlatform(t *testing.T) {
	if _, err := Python(context.Background(), t.TempDir(), "", Options{GOOS: "plan9", GOARCH: "386"}); err == nil {
		t.Fatal("want error")
	}
}

func TestPinnedAssetsAreOurTargets(t *testing.T) {
	for _, c := range [][2]string{{"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "arm64"}, {"windows", "amd64"}} {
		tr, err := Triple(c[0], c[1])
		if err != nil {
			t.Fatal(err)
		}
		if len(pinned[tr]) != 64 {
			t.Errorf("no pinned digest for %s", tr)
		}
	}
	if AssetName("3.12.15", "20261009", "aarch64-apple-darwin") != "cpython-3.12.15+20261009-aarch64-apple-darwin-install_only.tar.gz" {
		t.Error("asset name")
	}
}
