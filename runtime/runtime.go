// Package runtime lays a private Python interpreter (python-build-standalone)
// under an install root, so a product never touches the system Python.
package runtime

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/CharlesMod/wasp/engine"
	"github.com/CharlesMod/wasp/fetch"
)

// The pinned release and interpreter.
const (
	Tag            = "20261009"
	DefaultVersion = "3.12.15"
	DefaultBaseURL = "https://github.com/astral-sh/python-build-standalone/releases/download"
)

// pinned holds the sha256 of each install_only asset for Tag and
// DefaultVersion, as the release's SHA256SUMS lists them. With the default
// base URL both the downloaded SHA256SUMS and these must agree.
var pinned = map[string]string{
	"aarch64-apple-darwin":      "83dcb04777c798bd95f3687acf9ae84b09860272ee6bdfb05be8bcf9447f75b8",
	"aarch64-unknown-linux-gnu": "b342ccfb577fda349d0237c98b24848234ca767c10f034628a5ff9fc858d662f",
	"x86_64-pc-windows-msvc":    "194c8f0d9e0de6f1c4576e5dfa67819a48c764be87608b74b8ad43387a57b93b",
	"x86_64-unknown-linux-gnu":  "5864001bf969db329fadc2b654c4878c6027d170c6bd77e28f88666260bcaf63",
}

// Options shapes one install. The zero value installs the pinned build for
// this machine from GitHub.
type Options struct {
	BaseURL  string // releases/download root; tests point it at an httptest server
	Tag      string // release tag; default Tag
	GOOS     string // default runtime.GOOS
	GOARCH   string // default runtime.GOARCH
	Client   *http.Client
	Progress fetch.Progress
}

// Triple is the python-build-standalone target for an OS and arch.
func Triple(goos, goarch string) (string, error) {
	switch goos + "/" + goarch {
	case "linux/amd64":
		return "x86_64-unknown-linux-gnu", nil
	case "linux/arm64":
		return "aarch64-unknown-linux-gnu", nil
	case "darwin/arm64":
		return "aarch64-apple-darwin", nil
	case "darwin/amd64":
		return "x86_64-apple-darwin", nil
	case "windows/amd64":
		return "x86_64-pc-windows-msvc", nil
	}
	return "", fmt.Errorf("runtime: no private Python build for %s/%s", goos, goarch)
}

// AssetName is the release asset for a version, tag and target.
func AssetName(version, tag, triple string) string {
	return fmt.Sprintf("cpython-%s+%s-%s-install_only.tar.gz", version, tag, triple)
}

// Interpreter is where the interpreter lives under dir.
func Interpreter(dir, goos string) string {
	if goos == "windows" {
		return filepath.Join(dir, "python", "python.exe")
	}
	return filepath.Join(dir, "python", "bin", "python3")
}

// Dir is where a version is laid under root.
func Dir(root, version, tag string) string {
	return filepath.Join(root, "python", version+"+"+tag)
}

// Python returns the interpreter for version ("" is DefaultVersion) under
// root, downloading and verifying it first when it is not there. Only
// DefaultVersion is pinned; another version needs an Options.Tag that has it.
func Python(ctx context.Context, root, version string, o Options) (string, error) {
	if version == "" {
		version = DefaultVersion
	}
	if o.Tag == "" {
		o.Tag = Tag
	}
	if o.GOOS == "" {
		o.GOOS = runtime.GOOS
	}
	if o.GOARCH == "" {
		o.GOARCH = runtime.GOARCH
	}
	base := o.BaseURL
	custom := base != ""
	if base == "" {
		base = DefaultBaseURL
	}
	triple, err := Triple(o.GOOS, o.GOARCH)
	if err != nil {
		return "", err
	}
	dir := Dir(root, version, o.Tag)
	exe := Interpreter(dir, o.GOOS)
	if _, err := os.Stat(exe); err == nil {
		return exe, nil
	}
	asset := AssetName(version, o.Tag, triple)
	relURL := strings.TrimRight(base, "/") + "/" + o.Tag + "/"

	sums, err := fetchSums(ctx, o.Client, relURL+"SHA256SUMS")
	if err != nil {
		return "", err
	}
	want, ok := sums[asset]
	if !ok {
		return "", fmt.Errorf("runtime: %s is not in the release's SHA256SUMS", asset)
	}
	if !custom && version == DefaultVersion && o.Tag == Tag {
		if p, ok := pinned[triple]; ok && p != want {
			return "", fmt.Errorf("runtime: SHA256SUMS lists %s for %s, not the pinned %s", want, asset, p)
		}
	}

	dl := filepath.Join(root, "python", "download")
	if err := os.MkdirAll(dl, 0o755); err != nil {
		return "", err
	}
	archive := filepath.Join(dl, asset)
	if _, err := fetch.Download(ctx, relURL+asset, archive, fetch.Options{
		SHA256: want, Client: o.Client, Progress: o.Progress}); err != nil {
		return "", err
	}
	tmp := dir + ".tmp"
	_ = os.RemoveAll(tmp)
	if err := engine.Extract(archive, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return "", err
	}
	_ = os.RemoveAll(dir)
	if err := os.Rename(tmp, dir); err != nil {
		return "", err
	}
	if _, err := os.Stat(exe); err != nil {
		return "", fmt.Errorf("runtime: %s unpacked but has no interpreter at %s", asset, exe)
	}
	_ = os.Remove(archive)
	return exe, nil
}

func fetchSums(ctx context.Context, c *http.Client, url string) (map[string]string, error) {
	if c == nil {
		c = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("runtime: %s: %s", url, resp.Status)
	}
	return parseSums(resp.Body)
}

func parseSums(r io.Reader) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 {
			out[strings.TrimPrefix(f[1], "*")] = strings.ToLower(f[0])
		}
	}
	if len(out) == 0 {
		return nil, errors.New("runtime: SHA256SUMS is empty")
	}
	return out, sc.Err()
}
