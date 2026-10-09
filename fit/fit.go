// Package fit says what can run on a machine and how well: the shapes a model
// can take (all on the card, experts in RAM, split, CPU, unified memory), the
// largest context each holds, and which models are locked and why.
//
// The constants are an honest first guess, to be replaced by measurement.
// They are exported, named and gathered in Tuning so a product or a test can
// move them.
package fit

import (
	"fmt"

	"github.com/CharlesMod/wasp/gguf"
	"github.com/CharlesMod/wasp/profile"
)

// GB is a decimal gigabyte, the unit model files are quoted in.
const GB = 1_000_000_000

// The fit constants. See Params.
const (
	// VRAMUsable is the share of a card's memory a model may fill.
	VRAMUsable = 0.95
	// KVBytesPerElement is a q8_0 KV cache element: 34 bytes per block of 32.
	KVBytesPerElement = 1.0625
	// CardOverheadBase is the compute buffers and CUDA/Vulkan context on the card.
	CardOverheadBase = 0.6 * GB
	// CardOverheadPerSlot is what each parallel slot adds to the card's overhead.
	CardOverheadPerSlot = 0.25 * GB
	// HostOverhead is the server's own memory in RAM.
	HostOverhead = 0.6 * GB
	// OSReserve is the RAM left for the OS (Linux, Windows).
	OSReserve = 2 * GB
	// OSReserveMac is the RAM left for macOS.
	OSReserveMac = 4 * GB
	// CtxStep is the granularity contexts are rounded down to.
	CtxStep = 1024
)

// Params gathers the constants so they can be overridden.
type Params struct {
	VRAMUsable          float64
	KVBytesPerElement   float64
	CardOverheadBase    uint64
	CardOverheadPerSlot uint64
	HostOverhead        uint64
	OSReserve           uint64
	OSReserveMac        uint64
	CtxStep             int
}

// Tuning is what Plan and Recommend read. Change it to override the guesses.
var Tuning = Params{VRAMUsable, KVBytesPerElement, CardOverheadBase, CardOverheadPerSlot,
	HostOverhead, OSReserve, OSReserveMac, CtxStep}

// Shape kinds.
const (
	GPU        = "gpu"         // all on the card
	MoEOffload = "moe-offload" // experts in RAM, the rest on the card
	Partial    = "partial"     // dense, some layers on the card
	CPU        = "cpu"         // all in RAM
	Unified    = "unified"     // Apple silicon: one memory
)

// Speeds.
const (
	Fast = "fast"
	OK   = "ok"
	Slow = "slow"
)

// Want is what the person asks of a model: a context per slot, and slots.
type Want struct {
	Ctx   int // tokens per slot
	Slots int
}

// Shape is one way to run a model on a machine.
type Shape struct {
	Kind      string `json:"kind"`
	Fits      bool   `json:"fits"`
	NeedsCard uint64 `json:"needs_card"` // bytes on the card (unified: of the shared memory)
	NeedsRAM  uint64 `json:"needs_ram"`  // bytes in system RAM
	Speed     string `json:"speed"`
	Reason    string `json:"reason"`
	Ctx       int    `json:"ctx"` // tokens per slot
	Slots     int    `json:"slots"`
	// GPULayers is how many layers go to the card; -1 is all of them.
	GPULayers int `json:"gpu_layers"`
	// MoECPULayers is how many layers keep their experts in RAM (moe-offload).
	MoECPULayers int `json:"moe_cpu_layers,omitempty"`
}

// OnCard reports whether the shape runs from the card (or unified memory), the
// shapes a recommended model must have.
func (s Shape) OnCard() bool { return s.Kind == GPU || s.Kind == MoEOffload || s.Kind == Unified }

// KVBytes is the KV cache for ctx tokens in each of slots slots. Layers that
// keep the whole context cost ctx; sliding-window layers cost the window (at
// most ctx).
func KVBytes(f gguf.Facts, ctx, slots int) uint64 {
	perTok := float64(f.KVHeads) * float64(f.KeyLen+f.ValLen) * Tuning.KVBytesPerElement
	win := f.Window
	if win > ctx || win == 0 {
		win = ctx
	}
	toks := float64(f.KVLayers)*float64(ctx) + float64(f.SlidingLayers)*float64(win)
	return uint64(toks * perTok * float64(slots))
}

func gb(n uint64) string { return fmt.Sprintf("%.1f GB", float64(n)/GB) }

func sub(a, b uint64) uint64 {
	if a < b {
		return 0
	}
	return a - b
}

// Plan lists every shape the model could take on the machine, best first:
// gpu (or unified), moe-offload or partial, then cpu. A shape that does not
// fit is listed with the reason. With several cards the largest is used.
func Plan(p profile.Profile, f gguf.Facts, w Want) []Shape {
	if w.Ctx <= 0 {
		w.Ctx = 8192
	}
	if w.Slots <= 0 {
		w.Slots = 1
	}
	t := Tuning
	kv := KVBytes(f, w.Ctx, w.Slots)
	total := uint64(f.TotalBytes)
	cardOH := t.CardOverheadBase + t.CardOverheadPerSlot*uint64(w.Slots)
	base := Shape{Ctx: w.Ctx, Slots: w.Slots, GPULayers: -1}

	if p.Unified {
		need := total + kv + cardOH + t.HostOverhead
		limit := p.MetalBudget()
		if p.RAM > t.OSReserveMac && p.RAM-t.OSReserveMac < limit {
			limit = p.RAM - t.OSReserveMac
		}
		s := base
		s.Kind, s.Speed, s.NeedsCard, s.NeedsRAM = Unified, Fast, need, need
		s.Fits = need <= limit
		if s.Fits {
			s.Reason = fmt.Sprintf("fits unified memory: %s of the %s the GPU may use", gb(need), gb(limit))
		} else {
			s.Reason = fmt.Sprintf("needs %s of unified memory; this one allows %s", gb(need), gb(limit))
		}
		return []Shape{s}
	}

	reserve := t.OSReserve
	if p.OS == "darwin" {
		reserve = t.OSReserveMac
	}
	ram := sub(p.RAMAvail, reserve)
	var out []Shape

	if g, ok := p.BestGPU(); ok {
		budget := uint64(float64(g.VRAM) * t.VRAMUsable)

		s := base
		s.Kind, s.Speed = GPU, Fast
		s.NeedsCard = total + kv + cardOH
		s.Fits = s.NeedsCard <= budget
		if s.Fits {
			s.Reason = fmt.Sprintf("all on the card: %s of %s", gb(s.NeedsCard), gb(budget))
		} else {
			s.Reason = fmt.Sprintf("needs %s on the card; this one has %s", gb(s.NeedsCard), gb(budget))
		}
		out = append(out, s)

		if f.MoE() {
			s := base
			s.Kind, s.Speed, s.MoECPULayers = MoEOffload, OK, f.Layers
			dense := sub(total, uint64(f.ExpertBytes))
			s.NeedsCard = dense + kv + cardOH
			s.NeedsRAM = uint64(f.ExpertBytes) + t.HostOverhead
			switch {
			case s.NeedsCard > budget:
				s.Reason = fmt.Sprintf("needs %s on the card even with the experts in RAM; this one has %s", gb(s.NeedsCard), gb(budget))
			case s.NeedsRAM > ram:
				s.Reason = fmt.Sprintf("needs %s of RAM for the experts; this one has %s free", gb(s.NeedsRAM), gb(ram))
			default:
				s.Fits = true
				s.Reason = fmt.Sprintf("experts in RAM (%s), the rest on the card (%s)", gb(s.NeedsRAM), gb(s.NeedsCard))
			}
			out = append(out, s)
		} else if f.Layers > 0 {
			s := base
			s.Kind, s.Speed = Partial, Slow
			perLayer := (total + kv) / uint64(f.Layers)
			room := sub(budget, cardOH)
			n := int(room / perLayer)
			if n >= f.Layers {
				n = f.Layers - 1
			}
			if n < 1 {
				s.GPULayers = 0
				s.NeedsRAM = total + kv + t.HostOverhead
				s.Reason = fmt.Sprintf("not even one layer fits the card (%s free after overhead)", gb(room))
			} else {
				s.GPULayers = n
				s.NeedsCard = uint64(n)*perLayer + cardOH
				s.NeedsRAM = uint64(f.Layers-n)*perLayer + t.HostOverhead
				if s.NeedsRAM > ram {
					s.Reason = fmt.Sprintf("%d of %d layers fit the card; the other %s need RAM and this one has %s free",
						n, f.Layers, gb(s.NeedsRAM), gb(ram))
				} else {
					s.Fits = true
					s.Reason = fmt.Sprintf("%d of %d layers on the card, the rest in RAM: slow", n, f.Layers)
				}
			}
			out = append(out, s)
		}
	}

	c := base
	c.Kind, c.Speed, c.GPULayers = CPU, Slow, 0
	c.NeedsRAM = total + kv + t.HostOverhead
	c.Fits = c.NeedsRAM <= ram
	if c.Fits {
		c.Reason = fmt.Sprintf("all in RAM: %s of %s free: slow", gb(c.NeedsRAM), gb(ram))
	} else {
		c.Reason = fmt.Sprintf("needs %s of RAM; this one has %s free", gb(c.NeedsRAM), gb(ram))
	}
	return append(out, c)
}

// Best is the first shape that fits, or false.
func Best(shapes []Shape) (Shape, bool) {
	for _, s := range shapes {
		if s.Fits {
			return s, true
		}
	}
	return Shape{}, false
}

func shapeOf(shapes []Shape, kind string) (Shape, bool) {
	for _, s := range shapes {
		if s.Kind == kind && s.Fits {
			return s, true
		}
	}
	return Shape{}, false
}

// MaxCtx is the largest context per slot, a multiple of CtxStep between min
// and limit, at which a shape of this kind still fits. Fit only gets harder
// as context grows, so it searches. It returns min if even that is the most.
func MaxCtx(p profile.Profile, f gguf.Facts, kind string, slots, min, limit int) int {
	fits := func(ctx int) bool {
		_, ok := shapeOf(Plan(p, f, Want{Ctx: ctx, Slots: slots}), kind)
		return ok
	}
	step := Tuning.CtxStep
	hi := limit / step * step
	if hi <= min {
		return min
	}
	if fits(hi) {
		return hi
	}
	lo := min // fits (the caller proved it), hi does not
	for hi-lo > step {
		mid := (lo + (hi-lo)/2) / step * step
		if mid <= lo {
			break
		}
		if fits(mid) {
			lo = mid
		} else {
			hi = mid
		}
	}
	return lo
}
