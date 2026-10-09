# Wasp: the shared installer kit (spec)

Wasp puts a working local AI service on a machine that has drivers and nothing
else. It is a Go module (`github.com/CharlesMod/wasp`, Go 1.23,
`CGO_ENABLED=0`, so every target cross-compiles) used by two products that
must each stand alone and work together when both are present:

- **the Hive's installer** (today `swarm/wasp` in the Hive repo; it migrates
  onto this module in a later phase), and
- **DBee Setup** (`dbee/installer`), the standalone doctor with its own
  ride-along model server.

Neither product depends on the other. Each finds and reuses what the other
installed: an engine or a model already on the machine is found by its sha256
and not downloaded again.

## Phase 1: the core packages (this phase)

### `profile`: what this machine is
`profile.Detect() Profile`:
- `OS` (linux, darwin, windows), `Arch`, `WSL` bool, `CPUs`, `RAM` (total and
  available bytes), `DiskFree` (bytes, for the install root),
- `GPUs []GPU{Vendor, Name, VRAM bytes, Driver, ComputeCap}` and `Unified bool`
  (Apple silicon: the GPU uses system RAM).
- Sources, no cgo:
  - NVIDIA: `nvidia-smi --query-gpu=name,memory.total,driver_version,compute_cap --format=csv,noheader,nounits` (port the Hive's `internal/nvsmi` locator, which also finds it under WSL's driver directories).
  - AMD/Intel on Linux: `/sys/class/drm/card*/device/mem_info_vram_total`.
  - Windows non-NVIDIA: the registry's `HardwareInformation.qwMemorySize` under the display class key (PowerShell `Get-ItemProperty`), because `Win32_VideoController.AdapterRAM` caps at 4 GB.
  - Apple: `sysctl hw.memsize`, `hw.optional.arm64`; `Unified=true`, and the GPU budget is the working set Metal allows (take 75% of RAM under 36 GB, 80% above).
  - RAM: `/proc/meminfo`, `sysctl hw.memsize` + `vm_stat`, Windows `Get-CimInstance Win32_OperatingSystem`.
- A `HIVE_PROFILE_JSON`/`WASP_PROFILE_JSON` file override, so tests and the sim can pose as any machine.

### `gguf`: a model's facts from its header
Port the Hive's `internal/gguf` (header and tensor infos). Add
`gguf.FetchHeader(url)`: read only the header over HTTP Range requests
(grow the range until the tensor infos parse), so a model's facts are known
before a byte of its weights is downloaded. `Facts{Arch, Layers, KVLayers,
KVHeads, KeyLen, ValLen, CtxTrain, Experts, ExpertsUsed, ExpertBytes,
TotalBytes}`. `KVLayers` counts only the layers that keep a KV cache (hybrid
models with `full_attention_interval` keep it on one layer in N; sliding
window layers keep the window).

### `catalog` + `fit`: what can run here, and how well
A catalog entry: `{name, label, hf_repo, hf_file, sha256, bytes, kind
(dense|moe), rank (higher is the better mind), min_ctx, samplers{...},
reasoning (none|effort), why}`. The products ship their own catalog JSON;
Wasp ships none of its own.

`fit.Plan(profile, model facts, want{ctx, slots}) []Shape`, each shape:
- `gpu` (all on the card): weights + KV + card overhead ≤ VRAM × 0.95
- `moe-offload` (MoE only, experts in system RAM): non-expert weights + KV +
  card overhead ≤ VRAM × 0.95 and expert bytes + host overhead ≤ available RAM
- `partial` (dense, some layers on the card): allowed, marked slow
- `cpu`: weights + KV + host overhead ≤ available RAM
- on Apple (unified): one budget: weights + KV + overheads ≤ the Metal working set, and ≤ RAM less a 4 GB reserve.

KV bytes = KVLayers × KVHeads × (KeyLen + ValLen) × ctx × slots × bytes per
element (q8_0: 1.0625). Card overhead 0.6 GB + 0.25 GB per slot; host overhead
0.6 GB; keep 2 GB of RAM for the OS (4 GB on macOS). These constants are named,
documented, and overridable; they are an honest first guess, to be replaced by
measurement.

Each shape says `fits`, `needs_card`, `needs_ram`, `speed` (fast | ok | slow:
gpu is fast, moe-offload ok, partial and cpu slow), and a one-line `reason` a
person can read: "needs 13.6 GB on the card; this one has 8.0".
`fit.Recommend(profile, catalog)` picks, for each model, its best fitting shape
and the largest context up to the model's training length that still fits it.
A model with no fitting shape at `min_ctx`, 1 slot is **locked**, with the
reason. The recommended model is the highest `rank` whose best shape is `gpu`
or `moe-offload`; if none, the highest rank that fits at all.

### `fetch`: downloads a person can trust
Resumable (`.part` + Range), sha256 verified on arrival (mismatch: keep the
file as `.bad`, fail), a progress callback, and `Find(sha256, roots...)`: look
for a file with that digest in known model roots before downloading (the
Hive's `~/.hive/models`, Wasp's own root), hashing only files whose size
matches.

### `engine`: the ride-along model server
Port `swarm/wasp/engine.go`'s flavour detection and GitHub asset map (Metal
on macOS, CUDA + cudart on Windows NVIDIA, Vulkan on Linux and on Windows
non-NVIDIA, CPU otherwise), pinned to llama.cpp **b11279** (the build the
Hive's fleet measured), with a `Source` interface: GitHub releases is the
default; the Hive plugs in its court and honey. Extract to
`<root>/engine/<tag>-<flavor>/`, record `engine.json` (tag, flavor, source,
sha256), find `llama-server`. `engine.Find(roots...)` finds an engine either
product installed (the Hive's `~/.hive/engine/current`, Wasp's root) and
reuses it when its tag is recent enough.
`engine.ServeArgs(model, shape, ctx, slots, samplers)`: the llama-server
command line (`--host 127.0.0.1 --port N -c ... -np ... -ngl ... --n-cpu-moe
...`, `--jinja`, the samplers), never bound beyond loopback by default.

### Install roots
- Linux: `~/.local/share/wasp/` (models, engine); macOS: `~/Library/Application
  Support/wasp/`; Windows: `%LOCALAPPDATA%\wasp\`. The Hive's `~/.hive/`
  roots are searched too; Wasp never writes into them.

### `cmd/wasp-look`
A small CLI for phase 1: prints the profile, then the catalog table (each
model: best shape, ctx, needs, speed, or LOCKED with the reason) for a
catalog JSON given by path. It is how a person or a test sees what the wizard
will offer.

### Tests
Each package: table tests on recorded inputs (nvidia-smi lines, /proc/meminfo,
sysctl output, a registry dump, a GGUF header fixture written by the test
itself, a fake HTTP server for Range fetches and resumes). The fit tests pin
named machines: "4 GB laptop GPU, 16 GB RAM", "8 GB GPU, 32 GB RAM", "16 GB GPU,
32 GB RAM", "no GPU, 8 GB RAM", "M-series 16 GB", "M-series 64 GB", against a
dense 4B, a 26B-A4B MoE and a dense 27B, and show which are locked and why.

## Later phases
- Phase 2: `wizard`: the loopback setup UI kit (the Hive's wizard pages,
  palette and long-polled step runner, the window opener: Edge app mode on
  Windows, `open` on macOS, xdg-open on Linux) with a Product interface
  (pages, stages); DBee Setup on it.
- Phase 3: `service`: install a background service (systemd user unit with
  linger, launchd LaunchAgent/Daemon, Windows scheduled task at boot or SCM);
  the private Python runtime (python-build-standalone) for DBee.
- Phase 4: the Hive's `swarm/wasp` moves onto this module.
