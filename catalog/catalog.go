// Package catalog is the list of models a product offers. Wasp ships none of
// its own: each product brings its catalog JSON.
package catalog

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/CharlesMod/wasp/gguf"
)

// Model kinds.
const (
	Dense = "dense"
	MoE   = "moe"
)

// DefaultMinCtx is the context a model must hold, at one slot, to be offered
// when its entry names none.
const DefaultMinCtx = 8192

// Entry is one model a product offers.
type Entry struct {
	Name      string             `json:"name"`
	Label     string             `json:"label,omitempty"`
	HFRepo    string             `json:"hf_repo"`
	HFFile    string             `json:"hf_file"`
	SHA256    string             `json:"sha256"`
	Bytes     int64              `json:"bytes"`
	Kind      string             `json:"kind"`                // dense | moe
	Rank      int                `json:"rank"`                // higher is the better mind
	MinCtx    int                `json:"min_ctx,omitempty"`   // context it must hold to be offered
	Samplers  map[string]float64 `json:"samplers,omitempty"`  // temp, top_p, top_k, min_p, repeat_penalty, presence_penalty
	Reasoning string             `json:"reasoning,omitempty"` // none | effort
	Why       string             `json:"why,omitempty"`
	// Facts, when the catalog carries them, spare a header fetch.
	Facts *gguf.Facts `json:"facts,omitempty"`
}

// Catalog is a product's models.
type Catalog struct {
	Models []Entry `json:"models"`
}

// URL is where the model's file lives (Hugging Face "resolve" URL, main branch).
func (e Entry) URL() string {
	return "https://huggingface.co/" + e.HFRepo + "/resolve/main/" + e.HFFile
}

// MinContext is the entry's MinCtx or the default.
func (e Entry) MinContext() int {
	if e.MinCtx > 0 {
		return e.MinCtx
	}
	return DefaultMinCtx
}

// Verifiable reports whether the entry carries the digest a download is checked against.
func (e Entry) Verifiable() bool { return len(strings.TrimPrefix(e.SHA256, "sha256:")) == 64 }

// Parse reads a catalog, which is either {"models":[...]} or a bare array.
func Parse(b []byte) (Catalog, error) {
	var c Catalog
	if t := strings.TrimSpace(string(b)); strings.HasPrefix(t, "[") {
		if err := json.Unmarshal(b, &c.Models); err != nil {
			return c, err
		}
	} else if err := json.Unmarshal(b, &c); err != nil {
		return c, err
	}
	return c, c.Validate()
}

// Load reads a catalog file.
func Load(path string) (Catalog, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Catalog{}, err
	}
	c, err := Parse(b)
	if err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// Validate refuses an entry a fit or a download cannot work from.
func (c Catalog) Validate() error {
	seen := map[string]bool{}
	for i, e := range c.Models {
		switch {
		case e.Name == "":
			return fmt.Errorf("model %d has no name", i)
		case seen[e.Name]:
			return fmt.Errorf("model %q appears twice", e.Name)
		case e.HFRepo == "" || e.HFFile == "":
			return fmt.Errorf("model %q needs hf_repo and hf_file", e.Name)
		case e.Kind != Dense && e.Kind != MoE:
			return fmt.Errorf("model %q: kind must be %q or %q, not %q", e.Name, Dense, MoE, e.Kind)
		case e.Bytes <= 0:
			return fmt.Errorf("model %q has no size", e.Name)
		}
		seen[e.Name] = true
	}
	return nil
}
