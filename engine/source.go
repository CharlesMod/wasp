package engine

import (
	"context"
	"net/http"
	"strings"

	"github.com/CharlesMod/wasp/fetch"
)

// Source is where engine archives come from. GitHub releases is the default;
// the Hive plugs in its court and honey.
type Source interface {
	// Name is recorded in engine.json ("github", "court", ...).
	Name() string
	// Fetch puts the archive for this tag and asset name at dest and returns
	// its sha256 (hex), computed on arrival.
	Fetch(ctx context.Context, tag, asset, dest string) (sha256 string, err error)
}

// GitHubBase is where llama.cpp's release assets live.
const GitHubBase = "https://github.com/ggml-org/llama.cpp/releases/download"

// GitHub is the llama.cpp release page as a Source.
type GitHub struct {
	BaseURL  string // default GitHubBase; tests point it at a fake
	Client   *http.Client
	Progress fetch.Progress
}

// Name implements Source.
func (GitHub) Name() string { return "github" }

// Fetch implements Source: <base>/<tag>/<asset>, resumable. Upstream publishes
// no digest the pin could check, so the computed one is returned and recorded.
func (g GitHub) Fetch(ctx context.Context, tag, asset, dest string) (string, error) {
	base := strings.TrimRight(g.BaseURL, "/")
	if base == "" {
		base = GitHubBase
	}
	return fetch.Download(ctx, base+"/"+tag+"/"+asset, dest,
		fetch.Options{Unverified: true, Client: g.Client, Progress: g.Progress})
}
