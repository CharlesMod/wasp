package fit

import (
	"fmt"

	"github.com/CharlesMod/wasp/catalog"
	"github.com/CharlesMod/wasp/profile"
)

// Verdict is what one catalog model comes to on a machine.
type Verdict struct {
	Entry   catalog.Entry
	Unknown bool    // no facts for it, so it cannot be judged
	Locked  bool    // no shape fits at its min_ctx, one slot
	Why     string  // for Locked or Unknown: the reason a person can read
	Best    Shape   // for an unlocked model: its best shape at Ctx
	Ctx     int     // the largest context (per slot) that shape holds, up to training length
	Shapes  []Shape // every shape at min_ctx, best first
}

// Recommendation is a catalog judged on a machine.
type Recommendation struct {
	Verdicts []Verdict
	Pick     int // index into Verdicts of the recommended model, or -1
}

// Recommend judges each model of the catalog on the machine: its best fitting
// shape and the largest context that shape holds, or locked with the reason.
// The pick is the highest rank whose best shape runs from the card (gpu,
// moe-offload, unified); when none does, the highest rank that fits at all.
func Recommend(p profile.Profile, c catalog.Catalog) Recommendation {
	r := Recommendation{Pick: -1}
	for _, e := range c.Models {
		r.Verdicts = append(r.Verdicts, judge(p, e))
	}
	for pass := 0; pass < 2 && r.Pick < 0; pass++ {
		for i, v := range r.Verdicts {
			if v.Unknown || v.Locked || pass == 0 && !v.Best.OnCard() {
				continue
			}
			if r.Pick < 0 || v.Entry.Rank > r.Verdicts[r.Pick].Entry.Rank {
				r.Pick = i
			}
		}
	}
	return r
}

func judge(p profile.Profile, e catalog.Entry) Verdict {
	v := Verdict{Entry: e}
	if e.Facts == nil {
		v.Unknown, v.Why = true, "no model facts (read the GGUF header first)"
		return v
	}
	f := *e.Facts
	if f.TotalBytes == 0 {
		f.TotalBytes = e.Bytes
	}
	min := e.MinContext()
	v.Shapes = Plan(p, f, Want{Ctx: min, Slots: 1})
	best, ok := Best(v.Shapes)
	if !ok {
		v.Locked = true
		v.Why = lockReason(v.Shapes, min)
		return v
	}
	limit := f.CtxTrain
	if limit < min {
		limit = min
	}
	v.Ctx = MaxCtx(p, f, best.Kind, 1, min, limit)
	v.Best, _ = shapeOf(Plan(p, f, Want{Ctx: v.Ctx, Slots: 1}), best.Kind)
	return v
}

// lockReason says why nothing fit: the reason of the shape that came closest
// to the card, and of the last resort.
func lockReason(shapes []Shape, ctx int) string {
	last := shapes[len(shapes)-1]
	if len(shapes) == 1 {
		return fmt.Sprintf("does not fit at %d tokens of context: %s", ctx, last.Reason)
	}
	return fmt.Sprintf("does not fit at %d tokens of context: %s; %s: %s",
		ctx, shapes[0].Reason, last.Kind, last.Reason)
}
