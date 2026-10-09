// Package engine is the ride-along model server: it picks the llama.cpp build
// for this machine, fetches it from a Source, extracts it under the install
// root, records what it put there, finds an engine either product already
// installed, and builds the llama-server command line for a model.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/CharlesMod/wasp/fetch"
	"github.com/CharlesMod/wasp/internal/atomicfile"
	"github.com/CharlesMod/wasp/profile"
)

// LlamaTag is the upstream llama.cpp build Wasp installs: the build the Hive's
// fleet measured. It never floats on "latest"; a bump is a change to this line.
const LlamaTag = "b11279"

// WinCUDAVersion is the CUDA toolkit version in upstream's Windows CUDA asset
// names (llama-<tag>-bin-win-cuda-<version>-x64.zip). The Hive's pin named
// 13.3; b11279 publishes 13.4 (and 12.4 for older drivers), so this is checked
// against the release at the pinned tag and moves with it.
const WinCUDAVersion = "13.4"

// Flavor is which llama.cpp build suits a machine.
type Flavor string

// The flavors.
const (
	CUDA   Flavor = "cuda"
	Vulkan Flavor = "vulkan"
	Metal  Flavor = "metal"
	CPU    Flavor = "cpu"
)

// FlavorEnv names a flavor outright, for the person and for tests.
const FlavorEnv = "WASP_ENGINE_FLAVOR"

// vulkanPresent says whether a Vulkan loader is installed; a var for tests.
var vulkanPresent = func() bool {
	if _, err := exec.LookPath("vulkaninfo"); err == nil {
		return true
	}
	_, err := os.Stat("/usr/share/vulkan")
	return err == nil
}

// DetectFlavor reads the profile: Metal on macOS; on Windows CUDA for NVIDIA
// and Vulkan for any other card; on Linux Vulkan for any card, since upstream
// ships no Linux CUDA prebuilt (a drivers-only box runs Vulkan on the same
// driver); CPU otherwise. WASP_ENGINE_FLAVOR overrides.
func DetectFlavor(p profile.Profile) Flavor {
	if f := Flavor(strings.TrimSpace(os.Getenv(FlavorEnv))); f != "" {
		return f
	}
	nvidia, anyGPU := false, len(p.GPUs) > 0
	for _, g := range p.GPUs {
		if g.Vendor == "nvidia" {
			nvidia = true
		}
	}
	switch {
	case p.OS == "darwin":
		return Metal
	case p.OS == "windows" && nvidia:
		return CUDA
	case anyGPU && p.OS != "darwin":
		return Vulkan
	case p.OS == "linux" && vulkanPresent():
		return Vulkan
	}
	return CPU
}

// Assets names the upstream archives for a build: the second entry of the
// Windows CUDA flavor is the cudart redistributable that must land beside the
// binaries.
func Assets(tag, goos, arch string, flavor Flavor) ([]string, error) {
	key := goos + "/" + arch + "/" + string(flavor)
	switch key {
	case "linux/amd64/cpu":
		return []string{fmt.Sprintf("llama-%s-bin-ubuntu-x64.tar.gz", tag)}, nil
	case "linux/amd64/vulkan":
		return []string{fmt.Sprintf("llama-%s-bin-ubuntu-vulkan-x64.tar.gz", tag)}, nil
	case "darwin/arm64/metal", "darwin/arm64/cpu":
		return []string{fmt.Sprintf("llama-%s-bin-macos-arm64.tar.gz", tag)}, nil
	case "darwin/amd64/metal", "darwin/amd64/cpu":
		return []string{fmt.Sprintf("llama-%s-bin-macos-x64.tar.gz", tag)}, nil
	case "windows/amd64/cpu":
		return []string{fmt.Sprintf("llama-%s-bin-win-cpu-x64.zip", tag)}, nil
	case "windows/amd64/vulkan":
		return []string{fmt.Sprintf("llama-%s-bin-win-vulkan-x64.zip", tag)}, nil
	case "windows/amd64/cuda":
		return []string{
			fmt.Sprintf("llama-%s-bin-win-cuda-%s-x64.zip", tag, WinCUDAVersion),
			fmt.Sprintf("cudart-llama-bin-win-cuda-%s-x64.zip", WinCUDAVersion),
		}, nil
	case "linux/amd64/cuda":
		return nil, errors.New("upstream ships no Linux CUDA prebuilt: use the vulkan flavor, or a build of your own")
	}
	return nil, fmt.Errorf("no known upstream artifact for %s/%s %s", goos, arch, flavor)
}

// Record is what an install writes beside the engine (engine.json). Hive
// records (version, digest) are read too.
type Record struct {
	Tag    string `json:"tag"`
	Flavor string `json:"flavor"`
	Source string `json:"source"` // github, or whatever the Source calls itself
	SHA256 string `json:"sha256"` // of the first archive, computed on arrival
	Server string `json:"server"` // absolute path to llama-server
	Dir    string `json:"-"`      // where the record was found
}

// UnmarshalJSON accepts both Wasp's keys and the Hive's.
func (r *Record) UnmarshalJSON(b []byte) error {
	var w struct {
		Tag, Version, Flavor, Source, SHA256, Digest, Server string
	}
	// Lower-case JSON keys match the exported field names case-insensitively.
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	r.Tag, r.Flavor, r.Source, r.Server = w.Tag, w.Flavor, w.Source, w.Server
	if r.Tag == "" {
		r.Tag = w.Version
	}
	r.SHA256 = strings.TrimPrefix(w.SHA256, "sha256:")
	if r.SHA256 == "" {
		r.SHA256 = strings.TrimPrefix(w.Digest, "sha256:")
	}
	return nil
}

// DefaultRoots are the engine directories searched, Wasp's own first: Wasp
// installs into the first and never writes into the Hive's.
func DefaultRoots() []string {
	return []string{filepath.Join(profile.InstallRoot(), "engine"), filepath.Join(profile.HiveRoot(), "engine")}
}

// InstallOptions says which build to install. The zero value is this machine's
// pinned build.
type InstallOptions struct {
	Tag      string // default LlamaTag
	Flavor   Flavor // default CPU
	GOOS     string // default runtime.GOOS
	GOARCH   string // default runtime.GOARCH
	Progress fetch.Progress
}

// Install puts the engine under <root>/engine/<tag>-<flavor>/ (root is the
// install root, e.g. profile.InstallRoot()) and returns its record. An engine
// already installed there is reused without a download.
func Install(ctx context.Context, root string, src Source, o InstallOptions) (Record, error) {
	if o.Tag == "" {
		o.Tag = LlamaTag
	}
	if o.Flavor == "" {
		o.Flavor = CPU
	}
	if o.GOOS == "" {
		o.GOOS = runtime.GOOS
	}
	if o.GOARCH == "" {
		o.GOARCH = runtime.GOARCH
	}
	assets, err := Assets(o.Tag, o.GOOS, o.GOARCH, o.Flavor)
	if err != nil {
		return Record{}, err
	}
	engineDir := filepath.Join(root, "engine")
	dir := filepath.Join(engineDir, o.Tag+"-"+string(o.Flavor))
	if rec, ok := readRecord(dir); ok && isFile(rec.Server) {
		return rec, nil
	}
	dl := filepath.Join(engineDir, ".download")
	if err := os.MkdirAll(dl, 0o755); err != nil {
		return Record{}, err
	}
	defer os.RemoveAll(dl)
	stage := dir + ".tmp"
	_ = os.RemoveAll(stage)
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return Record{}, err
	}
	defer os.RemoveAll(stage)

	var first string
	for _, a := range assets {
		path := filepath.Join(dl, a)
		sum, err := src.Fetch(ctx, o.Tag, a, path)
		if err != nil {
			return Record{}, fmt.Errorf("engine: fetch %s: %w", a, err)
		}
		if first == "" {
			first = sum
		}
		if err := Extract(path, stage); err != nil {
			return Record{}, fmt.Errorf("engine: extract %s: %w", a, err)
		}
	}
	server := FindServer(stage, o.GOOS)
	if server == "" {
		return Record{}, errors.New("engine: the archive holds no llama-server")
	}
	_ = os.Chmod(server, 0o755)
	_ = os.RemoveAll(dir)
	if err := os.Rename(stage, dir); err != nil {
		return Record{}, err
	}
	rec := Record{Tag: o.Tag, Flavor: string(o.Flavor), Source: src.Name(), SHA256: first,
		Server: filepath.Join(dir, mustRel(stage, server)), Dir: dir}
	b, _ := json.MarshalIndent(rec, "", " ")
	if err := atomicfile.Write(filepath.Join(dir, "engine.json"), b, 0o644); err != nil {
		return Record{}, err
	}
	return rec, nil
}

func mustRel(base, p string) string {
	r, err := filepath.Rel(base, p)
	if err != nil {
		return p
	}
	return r
}

// FindServer finds llama-server (llama-server.exe on Windows) anywhere under
// root, since upstream layouts differ.
func FindServer(root, goos string) string {
	want := "llama-server"
	if goos == "windows" {
		want += ".exe"
	}
	var found string
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && found == "" && !info.IsDir() && info.Name() == want {
			found = p
		}
		return nil
	})
	return found
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func readRecord(dir string) (Record, bool) {
	b, err := os.ReadFile(filepath.Join(dir, "engine.json"))
	if err != nil {
		return Record{}, false
	}
	var r Record
	if json.Unmarshal(b, &r) != nil || r.Tag == "" {
		return Record{}, false
	}
	r.Dir = dir
	if !isFile(r.Server) {
		// A record that moved with its directory: look beside it.
		r.Server = FindServer(dir, runtime.GOOS)
		if r.Server == "" {
			r.Server = FindServer(dir, "windows")
		}
	}
	return r, r.Server != ""
}

// TagNumber reads the build number of a llama.cpp tag ("b11279" is 11279), or
// false when the tag is not one.
func TagNumber(tag string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(tag), "b"))
	return n, err == nil && n > 0
}

// TagAtLeast reports whether tag is at least as recent as min.
func TagAtLeast(tag, min string) bool {
	a, ok := TagNumber(tag)
	b, ok2 := TagNumber(min)
	return ok && ok2 && a >= b
}

// Find looks for an engine either product installed, under the engine
// directories in roots (DefaultRoots()). A candidate is a directory holding an
// engine.json (Wasp's, or the Hive's at the engine root) or named
// <tag>-<flavor> with a llama-server in it. It must be at least minTag and, when
// flavor is not empty, of that flavor. The newest wins; ties go to the earlier root.
func Find(minTag string, flavor Flavor, roots ...string) (Record, bool) {
	var found []Record
	add := func(r Record, ok bool) {
		if !ok || !TagAtLeast(r.Tag, minTag) || flavor != "" && r.Flavor != string(flavor) {
			return
		}
		found = append(found, r)
	}
	for _, root := range roots {
		add(readRecord(root))
		ents, _ := os.ReadDir(root)
		for _, e := range ents {
			dir := filepath.Join(root, e.Name())
			if fi, err := os.Stat(dir); err != nil || !fi.IsDir() || strings.HasPrefix(e.Name(), ".") || strings.HasSuffix(e.Name(), ".tmp") {
				continue
			}
			if r, ok := readRecord(dir); ok {
				add(r, true)
				continue
			}
			tag, fl, ok := strings.Cut(e.Name(), "-")
			if _, isTag := TagNumber(tag); !ok || !isTag {
				continue
			}
			srv := FindServer(dir, runtime.GOOS)
			if srv == "" {
				srv = FindServer(dir, "windows")
			}
			add(Record{Tag: tag, Flavor: fl, Source: "found", Server: srv, Dir: dir}, srv != "")
		}
	}
	if len(found) == 0 {
		return Record{}, false
	}
	sort.SliceStable(found, func(i, j int) bool {
		a, _ := TagNumber(found[i].Tag)
		b, _ := TagNumber(found[j].Tag)
		return a > b
	})
	return found[0], true
}
