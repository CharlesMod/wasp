package gguf

import (
	"errors"
	"os"
	"strings"
)

// Facts is what a model is, as far as fitting it to a machine needs.
//
// KVLayers counts the layers that keep a KV cache for the whole context.
// Hybrid models (full_attention_interval) keep it on one layer in N; layers
// with a sliding window keep only the window, and are counted apart in
// SlidingLayers with their Window, so that KVLayers + SlidingLayers is every
// layer that has a cache at all.
type Facts struct {
	Arch          string `json:"arch"`
	Layers        int    `json:"layers"`
	KVLayers      int    `json:"kv_layers"`
	SlidingLayers int    `json:"sliding_layers,omitempty"`
	Window        int    `json:"window,omitempty"`
	KVHeads       int    `json:"kv_heads"`
	KeyLen        int    `json:"key_len"`
	ValLen        int    `json:"val_len"`
	CtxTrain      int    `json:"ctx_train"`
	Experts       int    `json:"experts,omitempty"`
	ExpertsUsed   int    `json:"experts_used,omitempty"`
	ExpertBytes   int64  `json:"expert_bytes,omitempty"`
	TotalBytes    int64  `json:"total_bytes"`
}

// MoE reports whether the model routes through experts.
func (f Facts) MoE() bool { return f.Experts > 1 }

// ErrNoGeometry is a header that lacks the keys a fit needs.
var ErrNoGeometry = errors.New("gguf: header lacks the layer geometry")

// Derive reads a model's facts out of a parsed header (read with tensors).
func Derive(h Header) (Facts, error) {
	var f Facts
	arch := h.Meta["general.architecture"].Str
	if arch == "" {
		return f, ErrNoGeometry
	}
	f.Arch = arch
	geti := func(k string) int { return int(h.Meta[arch+"."+k].Int) }
	f.Layers = geti("block_count")
	f.CtxTrain = geti("context_length")
	f.Experts = geti("expert_count")
	f.ExpertsUsed = geti("expert_used_count")
	heads := geti("attention.head_count")
	if f.Layers == 0 || heads == 0 && len(h.Meta[arch+".attention.head_count"].Ints) == 0 {
		return f, ErrNoGeometry
	}
	if v := h.Meta[arch+".attention.head_count"]; v.Kind == Ints && len(v.Ints) > 0 {
		heads = int(v.Ints[0])
		for _, n := range v.Ints {
			if int(n) > heads {
				heads = int(n)
			}
		}
	}

	// The layers that keep a cache, and the heads they keep.
	cached := f.Layers
	f.KVHeads = geti("attention.head_count_kv")
	if v := h.Meta[arch+".attention.head_count_kv"]; v.Kind == Ints {
		cached, f.KVHeads = 0, 0
		for _, n := range v.Ints {
			if n > 0 {
				cached++
			}
			if int(n) > f.KVHeads {
				f.KVHeads = int(n)
			}
		}
	} else if f.KVHeads == 0 {
		f.KVHeads = heads
	}
	if n := geti("full_attention_interval"); n > 1 {
		cached = f.Layers / n
		if cached == 0 {
			cached = 1
		}
	}
	f.KVLayers = cached

	// Sliding-window layers keep the window only.
	if w := geti("attention.sliding_window"); w > 0 {
		pat := h.Meta[arch+".attention.sliding_window_pattern"]
		sliding := 0
		switch {
		case pat.Kind == Ints:
			for _, n := range pat.Ints {
				if n != 0 {
					sliding++
				}
			}
		case pat.Kind == Int && pat.Int > 1:
			sliding = f.Layers - f.Layers/int(pat.Int)
		}
		if sliding > cached {
			sliding = cached
		}
		if sliding > 0 {
			f.SlidingLayers, f.Window = sliding, w
			f.KVLayers = cached - sliding
		}
	}

	f.KeyLen = geti("attention.key_length")
	f.ValLen = geti("attention.value_length")
	if emb := geti("embedding_length"); emb > 0 && heads > 0 {
		if f.KeyLen == 0 {
			f.KeyLen = emb / heads
		}
		if f.ValLen == 0 {
			f.ValLen = emb / heads
		}
	}
	if f.KeyLen == 0 || f.ValLen == 0 {
		return f, ErrNoGeometry
	}
	for _, t := range h.Tensors {
		b := t.Bytes()
		f.TotalBytes += b
		if strings.Contains(t.Name, "_exps") {
			f.ExpertBytes += b
		}
	}
	return f, nil
}

// ReadFile derives the facts of a GGUF file on disk.
func ReadFile(path string) (Facts, error) {
	fh, err := os.Open(path)
	if err != nil {
		return Facts{}, err
	}
	defer fh.Close()
	h, err := Read(fh, true)
	if err != nil {
		return Facts{}, err
	}
	f, err := Derive(h)
	if fi, serr := os.Stat(path); serr == nil && fi.Size() > f.TotalBytes {
		f.TotalBytes = fi.Size()
	}
	return f, err
}
