// Package profile says what this machine is: OS, CPU, memory, disk and GPUs,
// found with no cgo from the tools each platform already has.
package profile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// GPU is one graphics card as far as running a model is concerned.
type GPU struct {
	Vendor     string `json:"vendor"` // nvidia, amd, intel, apple, other
	Name       string `json:"name"`
	VRAM       uint64 `json:"vram"` // bytes; 0 when unmeasured
	Driver     string `json:"driver,omitempty"`
	ComputeCap string `json:"compute_cap,omitempty"`
}

// Profile is one machine.
type Profile struct {
	OS       string `json:"os"` // linux, darwin, windows
	Arch     string `json:"arch"`
	WSL      bool   `json:"wsl,omitempty"`
	CPUs     int    `json:"cpus"`
	RAM      uint64 `json:"ram"`       // total bytes
	RAMAvail uint64 `json:"ram_avail"` // available bytes
	DiskFree uint64 `json:"disk_free"` // bytes free where the install root lives
	GPUs     []GPU  `json:"gpus"`
	Unified  bool   `json:"unified,omitempty"` // Apple silicon: the GPU uses system RAM
}

// Metal working-set shares: the part of unified memory the GPU may use.
const (
	MetalShareSmall = 0.75     // under MetalSmallBelow
	MetalShareLarge = 0.80     // at or above it
	MetalSmallBelow = 36 << 30 // bytes of RAM
)

// MetalBudget is the working set Metal allows on a unified-memory machine.
func (p Profile) MetalBudget() uint64 {
	share := MetalShareLarge
	if p.RAM < MetalSmallBelow {
		share = MetalShareSmall
	}
	return uint64(float64(p.RAM) * share)
}

// BestGPU is the card with the most memory, or false when there is none with
// a measured size.
func (p Profile) BestGPU() (GPU, bool) {
	var best GPU
	ok := false
	for _, g := range p.GPUs {
		if g.VRAM > 0 && (!ok || g.VRAM > best.VRAM) {
			best, ok = g, true
		}
	}
	return best, ok
}

// OverrideEnv are the variables that name a JSON file posing as the machine,
// so tests and simulations can be any machine.
var OverrideEnv = []string{"WASP_PROFILE_JSON", "HIVE_PROFILE_JSON"}

// Detect profiles this machine, or loads the override file when one is named.
func Detect() Profile {
	for _, e := range OverrideEnv {
		if path := strings.TrimSpace(os.Getenv(e)); path != "" {
			if p, err := Load(path); err == nil {
				return p
			}
		}
	}
	p := Profile{OS: runtime.GOOS, Arch: runtime.GOARCH, CPUs: runtime.NumCPU()}
	p.WSL = detectWSL()
	detectMemory(&p)
	detectGPUs(&p)
	p.DiskFree = DiskFree(InstallRoot())
	return p
}

// Load reads a profile from a JSON file.
func Load(path string) (Profile, error) {
	var p Profile
	b, err := os.ReadFile(path)
	if err != nil {
		return p, err
	}
	return p, json.Unmarshal(b, &p)
}

// InstallRoot is where Wasp keeps its models and engine: ~/.local/share/wasp,
// ~/Library/Application Support/wasp or %LOCALAPPDATA%\wasp.
func InstallRoot() string { return installRoot(runtime.GOOS, os.Getenv, homeDir()) }

func installRoot(goos string, env func(string) string, home string) string {
	switch goos {
	case "windows":
		base := env("LOCALAPPDATA")
		if base == "" {
			base = filepath.Join(home, "AppData", "Local")
		}
		return filepath.Join(base, "wasp")
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "wasp")
	}
	base := env("XDG_DATA_HOME")
	if base == "" {
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "wasp")
}

func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}

// HiveRoot is the Hive's model and engine root, searched but never written.
func HiveRoot() string { return filepath.Join(homeDir(), ".hive") }

func detectWSL() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	b, err := os.ReadFile("/proc/version")
	return err == nil && isWSLVersion(string(b))
}

func isWSLVersion(s string) bool {
	s = strings.ToLower(s)
	return strings.Contains(s, "microsoft") || strings.Contains(s, "wsl")
}
