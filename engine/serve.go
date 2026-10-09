package engine

import (
	"sort"
	"strconv"

	"github.com/CharlesMod/wasp/fit"
)

// LoopbackHost is the only address a server binds to unless asked otherwise.
const LoopbackHost = "127.0.0.1"

// DefaultPort is llama-server's own default.
const DefaultPort = 8080

// ServeOptions are the parts of the command line that are not the model.
type ServeOptions struct {
	Host string // default LoopbackHost
	Port int    // default DefaultPort
}

// samplerFlags maps a catalog sampler name to its llama-server flag.
var samplerFlags = map[string]string{
	"temp":             "--temp",
	"top_p":            "--top-p",
	"top_k":            "--top-k",
	"min_p":            "--min-p",
	"repeat_penalty":   "--repeat-penalty",
	"presence_penalty": "--presence-penalty",
}

func num(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// ServeArgs builds the llama-server command line (without the binary) for a
// model in a shape. ctx is tokens per slot; llama-server divides -c among its
// slots, so -c is ctx x slots. The cache is q8_0, which is what the fit
// prices. The server binds to loopback unless the options say otherwise.
func ServeArgs(model string, sh fit.Shape, ctx, slots int, samplers map[string]float64, o ServeOptions) []string {
	if slots < 1 {
		slots = 1
	}
	if o.Host == "" {
		o.Host = LoopbackHost
	}
	if o.Port == 0 {
		o.Port = DefaultPort
	}
	ngl := sh.GPULayers
	if ngl < 0 {
		ngl = 999 // all of them
	}
	args := []string{"-m", model, "--host", o.Host, "--port", strconv.Itoa(o.Port),
		"-c", strconv.Itoa(ctx * slots), "-np", strconv.Itoa(slots),
		"-ngl", strconv.Itoa(ngl)}
	if sh.MoECPULayers > 0 {
		args = append(args, "--n-cpu-moe", strconv.Itoa(sh.MoECPULayers))
	}
	args = append(args, "-fa", "on", "-ctk", "q8_0", "-ctv", "q8_0", "--jinja")
	names := make([]string, 0, len(samplers))
	for k := range samplers {
		if _, ok := samplerFlags[k]; ok {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	for _, k := range names {
		args = append(args, samplerFlags[k], num(samplers[k]))
	}
	return args
}
