package profile

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// NvidiaDirs is where the NVIDIA driver puts nvidia-smi when PATH does not:
// WSL2 bind-mounts it at /usr/lib/wsl/lib, which no PATH names, and Windows
// drivers use System32 or the older NVSMI directory. Searched in order. A var
// so tests can point it at a temp dir.
var NvidiaDirs = []string{
	"/usr/lib/wsl/lib",
	`C:\Windows\System32`,
	`C:\Program Files\NVIDIA Corporation\NVSMI`,
	"/usr/lib/x86_64-linux-gnu",
	"/opt/nvidia/bin",
}

// ProbeBound is how long any one probe may take; a wedged driver must not
// hang the installer.
var ProbeBound = 8 * time.Second

// NvidiaSmiPath finds nvidia-smi: the WASP_NVIDIA_SMI / HIVE_NVIDIA_SMI
// override, PATH, then NvidiaDirs. "" when there is none; that says the probe
// is missing, not that there is no GPU.
func NvidiaSmiPath() string {
	for _, e := range []string{"WASP_NVIDIA_SMI", "HIVE_NVIDIA_SMI"} {
		if p := strings.TrimSpace(os.Getenv(e)); p != "" && isFile(p) {
			return p
		}
	}
	if p, err := exec.LookPath("nvidia-smi"); err == nil {
		return p
	}
	name := "nvidia-smi"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	for _, d := range NvidiaDirs {
		p := filepath.Join(d, name)
		fi, err := os.Stat(p)
		if err != nil || fi.IsDir() {
			continue
		}
		if runtime.GOOS != "windows" && fi.Mode()&0o111 == 0 {
			continue
		}
		return p
	}
	return ""
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// run is how Detect talks to the machine; a var so tests can fake it.
var run = func(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), ProbeBound)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	return string(out), err
}

// NvidiaQuery is the nvidia-smi query Detect runs.
var NvidiaQuery = []string{"--query-gpu=name,memory.total,driver_version,compute_cap",
	"--format=csv,noheader,nounits"}

func powershell(script string) (string, error) {
	return run("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
}

const winDisplayScript = `Get-ItemProperty 'HKLM:\SYSTEM\CurrentControlSet\Control\Class\{4d36e968-e325-11ce-bfc1-08002be10318}\0*' -ErrorAction SilentlyContinue | ` +
	`Select-Object DriverDesc,ProviderName,@{n='Mem';e={$_.'HardwareInformation.qwMemorySize'}} | ConvertTo-Json`

const winMemScript = `Get-CimInstance Win32_OperatingSystem | Select-Object TotalVisibleMemorySize,FreePhysicalMemory | ConvertTo-Json`

func detectMemory(p *Profile) {
	switch runtime.GOOS {
	case "linux":
		if b, err := os.ReadFile("/proc/meminfo"); err == nil {
			p.RAM, p.RAMAvail = ParseMeminfo(string(b))
		}
	case "darwin":
		if out, err := run("sysctl", "-n", "hw.memsize"); err == nil {
			p.RAM = ParseSysctlValue(out)
		}
		if out, err := run("vm_stat"); err == nil {
			p.RAMAvail = ParseVMStat(out)
		}
		if p.RAMAvail == 0 {
			p.RAMAvail = p.RAM / 2
		}
	case "windows":
		if out, err := powershell(winMemScript); err == nil {
			if t, a, err := ParseWindowsMemory(out); err == nil {
				p.RAM, p.RAMAvail = t, a
			}
		}
	}
}

func detectGPUs(p *Profile) {
	if runtime.GOOS == "darwin" && appleSilicon() {
		// Apple silicon: the GPU is the system RAM.
		name := "Apple silicon"
		if out, err := run("sysctl", "-n", "machdep.cpu.brand_string"); err == nil && strings.TrimSpace(out) != "" {
			name = strings.TrimSpace(out)
		}
		p.Unified = true
		p.GPUs = []GPU{{Vendor: "apple", Name: name, VRAM: p.MetalBudget()}}
		return
	}
	if smi := NvidiaSmiPath(); smi != "" {
		if out, err := run(smi, NvidiaQuery...); err == nil {
			p.GPUs = append(p.GPUs, ParseNvidiaSmi(out)...)
		}
	}
	switch runtime.GOOS {
	case "linux":
		p.GPUs = append(p.GPUs, ReadDRM("/sys/class/drm")...)
	case "windows":
		if out, err := powershell(winDisplayScript); err == nil {
			if gs, err := ParseWindowsDisplayClass(out); err == nil {
				for _, g := range gs {
					if g.Vendor != "nvidia" || !hasVendor(p.GPUs, "nvidia") {
						p.GPUs = append(p.GPUs, g)
					}
				}
			}
		}
	}
}

func hasVendor(gs []GPU, v string) bool {
	for _, g := range gs {
		if g.Vendor == v {
			return true
		}
	}
	return false
}

// appleSilicon asks the kernel, so an x86-64 build running under Rosetta still
// knows it is on an M-series machine.
func appleSilicon() bool {
	out, err := run("sysctl", "-n", "hw.optional.arm64")
	return runtime.GOARCH == "arm64" || err == nil && strings.TrimSpace(out) == "1"
}
