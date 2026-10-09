// Command wasp-look prints what Wasp sees: this machine's profile, then a
// catalog judged against it, each model's best shape, context, needs and
// speed, or LOCKED with the reason. It is how a person or a test sees what
// the wizard will offer.
//
//	wasp-look [--fetch-facts] [--dump-facts] [--profile FILE] CATALOG.json
//
// Facts come from an optional "facts" object in each catalog entry, or, with
// --fetch-facts, from the GGUF header read over HTTP Range (no weights are
// downloaded). --dump-facts prints the catalog with the facts filled in.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/CharlesMod/wasp/catalog"
	"github.com/CharlesMod/wasp/fit"
	"github.com/CharlesMod/wasp/gguf"
	"github.com/CharlesMod/wasp/profile"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, out, errw io.Writer) int {
	fs := flag.NewFlagSet("wasp-look", flag.ContinueOnError)
	fs.SetOutput(errw)
	fetch := fs.Bool("fetch-facts", false, "read missing model facts from the GGUF headers over HTTP Range")
	dump := fs.Bool("dump-facts", false, "print the catalog JSON with the facts filled in, and stop")
	prof := fs.String("profile", "", "pose as the machine in this profile JSON file")
	// Flags may follow the catalog path: pull the path out first.
	var flags []string
	var pos []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--profile" || a == "-profile":
			flags = append(flags, a)
			if i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		case strings.HasPrefix(a, "-"):
			flags = append(flags, a)
		default:
			pos = append(pos, a)
		}
	}
	if err := fs.Parse(append(flags, pos...)); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(errw, "usage: wasp-look [--fetch-facts] [--dump-facts] [--profile FILE] CATALOG.json")
		return 2
	}
	cat, err := catalog.Load(rest[0])
	if err != nil {
		fmt.Fprintln(errw, "wasp-look:", err)
		return 1
	}
	if *fetch {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		for i := range cat.Models {
			e := &cat.Models[i]
			if e.Facts != nil {
				continue
			}
			f, err := gguf.FetchHeader(ctx, nil, e.URL())
			if err != nil {
				fmt.Fprintf(errw, "wasp-look: %s: %v\n", e.Name, err)
				continue
			}
			e.Facts = &f
		}
	}
	if *dump {
		b, _ := json.MarshalIndent(cat, "", "  ")
		fmt.Fprintln(out, string(b))
		return 0
	}
	if *prof != "" {
		os.Setenv("WASP_PROFILE_JSON", *prof)
	}
	p := profile.Detect()
	printProfile(out, p)
	fmt.Fprintln(out)
	printTable(out, p, cat)
	return 0
}

func gb(n uint64) string { return fmt.Sprintf("%.1f GB", float64(n)/1e9) }

func printProfile(w io.Writer, p profile.Profile) {
	wsl := ""
	if p.WSL {
		wsl = " (WSL)"
	}
	fmt.Fprintf(w, "machine   %s/%s%s, %d CPUs\n", p.OS, p.Arch, wsl, p.CPUs)
	fmt.Fprintf(w, "memory    %s RAM, %s available\n", gb(p.RAM), gb(p.RAMAvail))
	fmt.Fprintf(w, "disk      %s free for the install root\n", gb(p.DiskFree))
	if p.Unified {
		fmt.Fprintf(w, "unified   the GPU may use %s (Metal working set)\n", gb(p.MetalBudget()))
	}
	if len(p.GPUs) == 0 {
		fmt.Fprintln(w, "gpu       none found")
	}
	for _, g := range p.GPUs {
		extra := ""
		if g.Driver != "" {
			extra += ", driver " + g.Driver
		}
		if g.ComputeCap != "" {
			extra += ", compute " + g.ComputeCap
		}
		fmt.Fprintf(w, "gpu       %s %s, %s%s\n", g.Vendor, g.Name, gb(g.VRAM), extra)
	}
}

func printTable(w io.Writer, p profile.Profile, c catalog.Catalog) {
	r := fit.Recommend(p, c)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "MODEL\tRANK\tSIZE\tSHAPE\tCTX\tCARD\tRAM\tSPEED\t")
	for i, v := range r.Verdicts {
		e := v.Entry
		name := e.Name
		if i == r.Pick {
			name += " *"
		}
		size := gb(uint64(e.Bytes))
		switch {
		case v.Unknown:
			fmt.Fprintf(tw, "%s\t%d\t%s\tUNKNOWN\t\t\t\t\t\n", name, e.Rank, size)
		case v.Locked:
			fmt.Fprintf(tw, "%s\t%d\t%s\tLOCKED\t\t\t\t\t\n", name, e.Rank, size)
		default:
			b := v.Best
			fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%d\t%s\t%s\t%s\t\n", name, e.Rank, size, b.Kind, v.Ctx,
				gb(b.NeedsCard), gb(b.NeedsRAM), b.Speed)
		}
	}
	tw.Flush()
	fmt.Fprintln(w)
	for _, v := range r.Verdicts {
		switch {
		case v.Unknown:
			fmt.Fprintf(w, "%s: %s (try --fetch-facts)\n", v.Entry.Name, v.Why)
		case v.Locked:
			fmt.Fprintf(w, "%s: LOCKED, %s\n", v.Entry.Name, v.Why)
		default:
			fmt.Fprintf(w, "%s: %s\n", v.Entry.Name, v.Best.Reason)
		}
	}
	if r.Pick >= 0 {
		fmt.Fprintf(w, "\n* recommended: %s\n", r.Verdicts[r.Pick].Entry.Name)
	} else {
		fmt.Fprintln(w, "\nno model can run on this machine")
	}
}
