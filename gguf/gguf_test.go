package gguf

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fixture writes a minimal valid GGUF v3 header.
type fixture struct {
	buf bytes.Buffer
	kv  int
	kvb bytes.Buffer
	tb  bytes.Buffer
	nt  int
}

func (f *fixture) u32(b *bytes.Buffer, v uint32) { _ = binary.Write(b, binary.LittleEndian, v) }
func (f *fixture) u64(b *bytes.Buffer, v uint64) { _ = binary.Write(b, binary.LittleEndian, v) }
func (f *fixture) str(b *bytes.Buffer, s string) {
	f.u64(b, uint64(len(s)))
	b.WriteString(s)
}
func (f *fixture) setU32(k string, v uint32) {
	f.str(&f.kvb, k)
	f.u32(&f.kvb, typeUint32)
	f.u32(&f.kvb, v)
	f.kv++
}
func (f *fixture) setStr(k, v string) {
	f.str(&f.kvb, k)
	f.u32(&f.kvb, typeString)
	f.str(&f.kvb, v)
	f.kv++
}
func (f *fixture) setF32(k string) {
	f.str(&f.kvb, k)
	f.u32(&f.kvb, typeFloat32)
	f.u32(&f.kvb, 0x3f800000)
	f.kv++
}
func (f *fixture) setInts(k string, vs ...uint32) {
	f.str(&f.kvb, k)
	f.u32(&f.kvb, typeArray)
	f.u32(&f.kvb, typeUint32)
	f.u64(&f.kvb, uint64(len(vs)))
	for _, v := range vs {
		f.u32(&f.kvb, v)
	}
	f.kv++
}

// setVocab adds an array of n strings, as a vocabulary does, to make the header big.
func (f *fixture) setVocab(n int) {
	f.str(&f.kvb, "tokenizer.ggml.tokens")
	f.u32(&f.kvb, typeArray)
	f.u32(&f.kvb, typeString)
	f.u64(&f.kvb, uint64(n))
	for i := 0; i < n; i++ {
		f.str(&f.kvb, "token-"+strconv.Itoa(i))
	}
	f.kv++
}
func (f *fixture) tensor(name string, typ uint32, dims ...uint64) {
	f.str(&f.tb, name)
	f.u32(&f.tb, uint32(len(dims)))
	for _, d := range dims {
		f.u64(&f.tb, d)
	}
	f.u32(&f.tb, typ)
	f.u64(&f.tb, 0)
	f.nt++
}
func (f *fixture) bytes() []byte {
	var out bytes.Buffer
	out.WriteString("GGUF")
	f.u32(&out, 3)
	f.u64(&out, uint64(f.nt))
	f.u64(&out, uint64(f.kv))
	out.Write(f.kvb.Bytes())
	out.Write(f.tb.Bytes())
	return out.Bytes()
}

func moeFixture() *fixture {
	f := &fixture{}
	f.setStr("general.architecture", "tinymoe")
	f.setU32("tinymoe.block_count", 6)
	f.setU32("tinymoe.context_length", 65536)
	f.setU32("tinymoe.attention.head_count", 16)
	f.setU32("tinymoe.attention.head_count_kv", 4)
	f.setU32("tinymoe.attention.key_length", 128)
	f.setU32("tinymoe.attention.value_length", 128)
	f.setU32("tinymoe.expert_count", 8)
	f.setU32("tinymoe.expert_used_count", 2)
	f.setU32("tinymoe.attention.sliding_window", 512)
	f.setU32("tinymoe.attention.sliding_window_pattern", 3) // every 3rd layer is global
	f.setF32("tinymoe.rope.freq_base")
	f.tensor("token_embd.weight", 8, 256, 100)         // Q8_0: 256*100/32*34
	f.tensor("blk.0.attn_q.weight", 12, 256, 256)      // Q4_K
	f.tensor("blk.0.ffn_up_exps.weight", 21, 256, 8*4) // IQ3_S
	f.tensor("blk.0.ffn_down_exps.weight", 21, 256, 8*4)
	return f
}

func TestReadAndDeriveMoE(t *testing.T) {
	f := moeFixture()
	h, err := Read(bytes.NewReader(f.bytes()), true)
	if err != nil {
		t.Fatal(err)
	}
	if h.Version != 3 || len(h.Tensors) != 4 {
		t.Fatalf("version %d tensors %d", h.Version, len(h.Tensors))
	}
	got, err := Derive(h)
	if err != nil {
		t.Fatal(err)
	}
	expert := int64(256*32/256*110) * 2
	want := Facts{Arch: "tinymoe", Layers: 6, KVLayers: 2, SlidingLayers: 4, Window: 512,
		KVHeads: 4, KeyLen: 128, ValLen: 128, CtxTrain: 65536, Experts: 8, ExpertsUsed: 2,
		ExpertBytes: expert, TotalBytes: int64(256*100/32*34) + int64(256*256/256*144) + expert}
	if got != want {
		t.Fatalf("facts\n got %+v\nwant %+v", got, want)
	}
	if !got.MoE() {
		t.Fatal("not MoE")
	}
}

func TestDeriveHybridInterval(t *testing.T) {
	f := &fixture{}
	f.setStr("general.architecture", "hyb")
	f.setU32("hyb.block_count", 64)
	f.setU32("hyb.context_length", 262144)
	f.setU32("hyb.attention.head_count", 24)
	f.setU32("hyb.attention.head_count_kv", 4)
	f.setU32("hyb.attention.key_length", 256)
	f.setU32("hyb.attention.value_length", 256)
	f.setU32("hyb.full_attention_interval", 4)
	f.tensor("output.weight", 0, 64, 64)
	h, err := Read(bytes.NewReader(f.bytes()), true)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Derive(h)
	if err != nil {
		t.Fatal(err)
	}
	if got.KVLayers != 16 || got.SlidingLayers != 0 || got.Experts != 0 || got.MoE() {
		t.Fatalf("%+v", got)
	}
}

func TestDeriveKVHeadsPerLayerArray(t *testing.T) {
	f := &fixture{}
	f.setStr("general.architecture", "arr")
	f.setU32("arr.block_count", 4)
	f.setU32("arr.attention.head_count", 8)
	f.setInts("arr.attention.head_count_kv", 0, 2, 0, 2)
	f.setU32("arr.embedding_length", 512)
	h, err := Read(bytes.NewReader(f.bytes()), true)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Derive(h)
	if err != nil {
		t.Fatal(err)
	}
	if got.KVLayers != 2 || got.KVHeads != 2 || got.KeyLen != 64 {
		t.Fatalf("%+v", got)
	}
}

func TestReadRefusals(t *testing.T) {
	if _, err := Read(strings.NewReader("nope"), false); err != ErrNotGGUF {
		t.Fatalf("want ErrNotGGUF, got %v", err)
	}
	b := moeFixture().bytes()
	binary.LittleEndian.PutUint32(b[4:], 1)
	if _, err := Read(bytes.NewReader(b), false); err == nil {
		t.Fatal("v1 accepted")
	}
	var empty fixture
	empty.setStr("general.name", "x")
	h, _ := Read(bytes.NewReader(empty.bytes()), true)
	if _, err := Derive(h); err != ErrNoGeometry {
		t.Fatalf("want ErrNoGeometry, got %v", err)
	}
}

func TestReadFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "m.gguf")
	if err := os.WriteFile(p, moeFixture().bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := ReadFile(p)
	if err != nil || f.Layers != 6 {
		t.Fatalf("%+v %v", f, err)
	}
}

// rangeServer serves body honouring Range, counting requests.
func rangeServer(body []byte, total int64, hits *int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hits++
		var from, to int64
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &from, &to); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if to >= int64(len(body)) {
			to = int64(len(body)) - 1
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, to, total))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(body[from : to+1])
	}))
}

func TestFetchHeaderGrowsTheRange(t *testing.T) {
	f := moeFixture()
	f.setVocab(300) // a header bigger than the first range
	body := f.bytes()
	body = append(body, make([]byte, 5000)...) // some "weights"
	old := InitialRange
	InitialRange = 1024
	defer func() { InitialRange = old }()
	if len(body) < 3000 {
		t.Fatalf("fixture too small to test growth: %d", len(body))
	}
	hits := 0
	srv := rangeServer(body, 11_289_671_136, &hits)
	defer srv.Close()
	got, err := FetchHeader(context.Background(), srv.Client(), srv.URL+"/m.gguf")
	if err != nil {
		t.Fatal(err)
	}
	if hits < 2 {
		t.Fatalf("expected the range to grow, %d request(s)", hits)
	}
	if got.Layers != 6 || got.TotalBytes != 11_289_671_136 {
		t.Fatalf("%+v", got)
	}
}

func TestFetchHeaderTruncatedFileFails(t *testing.T) {
	body := moeFixture().bytes()[:100]
	hits := 0
	srv := rangeServer(body, int64(len(body)), &hits)
	defer srv.Close()
	if _, err := FetchHeader(context.Background(), srv.Client(), srv.URL); err == nil {
		t.Fatal("truncated header accepted")
	}
}
