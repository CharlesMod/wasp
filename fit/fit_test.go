package fit

import (
	"strings"
	"testing"

	"github.com/CharlesMod/wasp/catalog"
	"github.com/CharlesMod/wasp/gguf"
	"github.com/CharlesMod/wasp/profile"
)

const gib = 1 << 30

// The three synthetic models: facts, not real files.
var (
	// dense 4B: 2.5 GB, 32 layers all keeping a cache, 8 KV heads of 128.
	dense4B = gguf.Facts{Arch: "dense", Layers: 32, KVLayers: 32, KVHeads: 8, KeyLen: 128, ValLen: 128,
		CtxTrain: 262144, TotalBytes: 2_500_000_000}
	// 26B-A4B MoE: 11.3 GB, 85% of it experts, 30 layers of which 5 keep the
	// whole context and 25 a 1024-token window.
	moe26B = gguf.Facts{Arch: "moe", Layers: 30, KVLayers: 5, SlidingLayers: 25, Window: 1024, KVHeads: 8,
		KeyLen: 256, ValLen: 256, CtxTrain: 262144, Experts: 128, ExpertsUsed: 8,
		ExpertBytes: 9_605_000_000, TotalBytes: 11_300_000_000}
	// dense 27B: 13 GB, 64 layers, full attention on every 4th, 4 KV heads of 256.
	dense27B = gguf.Facts{Arch: "hybrid", Layers: 64, KVLayers: 16, KVHeads: 4, KeyLen: 256, ValLen: 256,
		CtxTrain: 262144, TotalBytes: 13_000_000_000}
)

// The named machines. RAM figures are what is free, GiB each; cards are GiB.
func machine(name string) profile.Profile {
	nv := func(vram uint64) []profile.GPU {
		return []profile.GPU{{Vendor: "nvidia", Name: "test card", VRAM: vram * gib}}
	}
	switch name {
	case "4 GB laptop GPU, 16 GB RAM":
		return profile.Profile{OS: "linux", RAM: 16 * gib, RAMAvail: 14 * gib, GPUs: nv(4)}
	case "8 GB GPU, 32 GB RAM":
		return profile.Profile{OS: "linux", RAM: 32 * gib, RAMAvail: 28 * gib, GPUs: nv(8)}
	case "16 GB GPU, 32 GB RAM":
		return profile.Profile{OS: "linux", RAM: 32 * gib, RAMAvail: 28 * gib, GPUs: nv(16)}
	case "no GPU, 8 GB RAM":
		return profile.Profile{OS: "linux", RAM: 8 * gib, RAMAvail: 6 * gib}
	case "M-series 16 GB":
		p := profile.Profile{OS: "darwin", RAM: 16 * gib, RAMAvail: 10 * gib, Unified: true}
		p.GPUs = []profile.GPU{{Vendor: "apple", Name: "M", VRAM: p.MetalBudget()}}
		return p
	case "M-series 64 GB":
		p := profile.Profile{OS: "darwin", RAM: 64 * gib, RAMAvail: 40 * gib, Unified: true}
		p.GPUs = []profile.GPU{{Vendor: "apple", Name: "M", VRAM: p.MetalBudget()}}
		return p
	}
	panic(name)
}

func entry(name, kind string, rank int, f gguf.Facts) catalog.Entry {
	f2 := f
	return catalog.Entry{Name: name, HFRepo: "o/r", HFFile: name + ".gguf", Bytes: f.TotalBytes,
		Kind: kind, Rank: rank, MinCtx: 8192, Facts: &f2}
}

func cat() catalog.Catalog {
	return catalog.Catalog{Models: []catalog.Entry{
		entry("dense-4b", "dense", 1, dense4B),
		entry("swift-27b", "dense", 2, dense27B),
		entry("moe-26b", "moe", 3, moe26B),
	}}
}

func TestKVBytes(t *testing.T) {
	// 32 layers x 8 heads x 256 x 1.0625 B = 69,632 B a token.
	if got := KVBytes(dense4B, 1000, 1); got != 69_632_000 {
		t.Fatalf("dense 4B: %d", got)
	}
	if got := KVBytes(dense4B, 1000, 3); got != 3*69_632_000 {
		t.Fatalf("slots: %d", got)
	}
	// MoE: 5 full layers (21,760 B/token) + 25 windowed (4,352 B/token x 1024).
	want := uint64(21_760*8192 + 25*1024*4352)
	if got := KVBytes(moe26B, 8192, 1); got != want {
		t.Fatalf("moe: %d != %d", got, want)
	}
	// The window never costs more than the context.
	short := uint64(30 * 512 * 4352)
	if got := KVBytes(moe26B, 512, 1); got != short {
		t.Fatalf("short ctx: %d != %d", got, short)
	}
}

func TestPlanShapesAndReasons(t *testing.T) {
	small := machine("4 GB laptop GPU, 16 GB RAM")
	shapes := Plan(small, dense27B, Want{Ctx: 8192, Slots: 1})
	kinds := []string{}
	for _, s := range shapes {
		kinds = append(kinds, s.Kind)
	}
	if strings.Join(kinds, ",") != "gpu,partial,cpu" {
		t.Fatalf("dense kinds %v", kinds)
	}
	if shapes[0].Fits || !strings.Contains(shapes[0].Reason, "on the card; this one has") {
		t.Fatalf("gpu: %+v", shapes[0])
	}
	// 4 GiB x 0.95 = 4.08 GB, less 0.85 GB overhead, over 207.5 MB a layer: 15 layers.
	if p := shapes[1]; !p.Fits || p.GPULayers != 15 || p.Speed != Slow {
		t.Fatalf("partial: %+v", p)
	}
	moe := Plan(small, moe26B, Want{Ctx: 8192, Slots: 1})
	if moe[1].Kind != MoEOffload || !moe[1].Fits || moe[1].Speed != OK || moe[1].MoECPULayers != 30 {
		t.Fatalf("moe-offload: %+v", moe[1])
	}
	// No card: only the CPU shape exists.
	if cpu := Plan(machine("no GPU, 8 GB RAM"), dense4B, Want{}); len(cpu) != 1 || cpu[0].Kind != CPU {
		t.Fatalf("cpu: %+v", cpu)
	}
}

func TestSlotsCostCardOverheadAndCache(t *testing.T) {
	m := machine("16 GB GPU, 32 GB RAM")
	one, _ := Best(Plan(m, dense4B, Want{Ctx: 8192, Slots: 1}))
	four, _ := Best(Plan(m, dense4B, Want{Ctx: 8192, Slots: 4}))
	// Three more slots: 3 x 0.25 GB overhead and 3 x the cache.
	if four.NeedsCard-one.NeedsCard != 3*CardOverheadPerSlot+3*KVBytes(dense4B, 8192, 1) {
		t.Fatalf("one %d four %d", one.NeedsCard, four.NeedsCard)
	}
}

func TestEveryNamedMachineAgainstEveryModel(t *testing.T) {
	type want struct {
		kind   string // best shape, or "" for locked
		locked string // substring of the lock reason
	}
	cases := []struct {
		machine string
		models  map[string]want
		pick    string
	}{
		{"4 GB laptop GPU, 16 GB RAM",
			map[string]want{"dense-4b": {GPU, ""}, "swift-27b": {Partial, ""}, "moe-26b": {MoEOffload, ""}}, "moe-26b"},
		{"8 GB GPU, 32 GB RAM",
			map[string]want{"dense-4b": {GPU, ""}, "swift-27b": {Partial, ""}, "moe-26b": {MoEOffload, ""}}, "moe-26b"},
		{"16 GB GPU, 32 GB RAM",
			map[string]want{"dense-4b": {GPU, ""}, "swift-27b": {GPU, ""}, "moe-26b": {GPU, ""}}, "moe-26b"},
		{"no GPU, 8 GB RAM",
			map[string]want{"dense-4b": {CPU, ""}, "swift-27b": {"", "needs 13.9 GB of RAM; this one has 4.4 GB free"},
				"moe-26b": {"", "needs 12.2 GB of RAM; this one has 4.4 GB free"}}, "dense-4b"},
		// 16 GiB x 0.75 = 12.9 GB of working set: the 26B's 13.0 GB does not fit.
		{"M-series 16 GB",
			map[string]want{"dense-4b": {Unified, ""}, "swift-27b": {"", "needs 14.7 GB of unified memory; this one allows 12.9 GB"},
				"moe-26b": {"", "needs 13.0 GB of unified memory; this one allows 12.9 GB"}}, "dense-4b"},
		{"M-series 64 GB",
			map[string]want{"dense-4b": {Unified, ""}, "swift-27b": {Unified, ""}, "moe-26b": {Unified, ""}}, "moe-26b"},
	}
	for _, c := range cases {
		t.Run(c.machine, func(t *testing.T) {
			p := machine(c.machine)
			r := Recommend(p, cat())
			for _, v := range r.Verdicts {
				w := c.models[v.Entry.Name]
				if w.kind == "" {
					if !v.Locked || !strings.Contains(v.Why, w.locked) {
						t.Errorf("%s should be locked with %q: %+v", v.Entry.Name, w.locked, v)
					}
					continue
				}
				if v.Locked || v.Best.Kind != w.kind {
					t.Errorf("%s: want %s, got locked=%v %+v (%s)", v.Entry.Name, w.kind, v.Locked, v.Best, v.Why)
					continue
				}
				// The context is the largest that fits: one more step does not.
				f := *v.Entry.Facts
				if _, ok := shapeOf(Plan(p, f, Want{Ctx: v.Ctx, Slots: 1}), w.kind); !ok {
					t.Errorf("%s: ctx %d does not fit", v.Entry.Name, v.Ctx)
				}
				if v.Ctx < f.CtxTrain {
					if _, ok := shapeOf(Plan(p, f, Want{Ctx: v.Ctx + CtxStep, Slots: 1}), w.kind); ok {
						t.Errorf("%s: ctx %d is not the largest", v.Entry.Name, v.Ctx)
					}
				}
			}
			if got := r.Verdicts[r.Pick].Entry.Name; got != c.pick {
				t.Errorf("recommended %s, want %s", got, c.pick)
			}
		})
	}
}

func TestContextsFollowFromTheFormulas(t *testing.T) {
	// 16 GiB card: budget 16.32 GB. The 27B (13 GB + 0.85 overhead) leaves
	// 2.47 GB of cache at 34,816 B a token: 70,9xx tokens, rounded to 70,656.
	r := Recommend(machine("16 GB GPU, 32 GB RAM"), cat())
	byName := map[string]Verdict{}
	for _, v := range r.Verdicts {
		byName[v.Entry.Name] = v
	}
	if got := byName["swift-27b"].Ctx; got != 70_656 {
		t.Errorf("27B ctx %d", got)
	}
	// The 4 GB laptop holds the 4B on the card to only 10,240 tokens.
	r = Recommend(machine("4 GB laptop GPU, 16 GB RAM"), cat())
	if got := r.Verdicts[0].Ctx; got != 10_240 {
		t.Errorf("4B on the laptop: %d", got)
	}
	// A model whose context fits entirely is capped at its training length.
	r = Recommend(machine("M-series 64 GB"), cat())
	if got := r.Verdicts[0].Ctx; got != 262_144 {
		t.Errorf("4B on 64 GB Mac: %d", got)
	}
}

func TestRecommendFallsBackToWhatFitsAtAll(t *testing.T) {
	// A card too small for any model's card shape: the pick is the best rank that runs at all.
	p := machine("no GPU, 8 GB RAM")
	p.RAMAvail = 20 * gib
	r := Recommend(p, cat())
	if got := r.Verdicts[r.Pick].Entry.Name; got != "moe-26b" {
		t.Fatalf("picked %s", got)
	}
	// Nothing fits: no pick.
	p.RAMAvail = 3 * gib
	if r := Recommend(p, cat()); r.Pick != -1 {
		t.Fatalf("pick %d with nothing fitting", r.Pick)
	}
}

func TestUnknownFactsAreNotLocked(t *testing.T) {
	c := catalog.Catalog{Models: []catalog.Entry{{Name: "x", HFRepo: "o/r", HFFile: "f", Bytes: 1, Kind: "dense"}}}
	r := Recommend(machine("16 GB GPU, 32 GB RAM"), c)
	if !r.Verdicts[0].Unknown || r.Verdicts[0].Locked || r.Pick != -1 {
		t.Fatalf("%+v", r)
	}
}

func TestTuningIsOverridable(t *testing.T) {
	old := Tuning
	defer func() { Tuning = old }()
	m := machine("16 GB GPU, 32 GB RAM")
	before, _ := Best(Plan(m, dense4B, Want{Ctx: 8192, Slots: 1}))
	Tuning.CardOverheadBase += GB
	after, _ := Best(Plan(m, dense4B, Want{Ctx: 8192, Slots: 1}))
	if after.NeedsCard-before.NeedsCard != GB {
		t.Fatal("overhead not applied")
	}
}
