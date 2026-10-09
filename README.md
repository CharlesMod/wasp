<p align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset="assets/banner-dark.png">
  <img alt="wasp, the installer kit" src="assets/banner-light.png" width="509">
</picture>
</p>

# wasp

The installer kit shared by the Hive and DBee. See SPEC.md.

Phase 1 packages: `profile` (this machine), `gguf` (a model's facts from its
header), `catalog` and `fit` (what can run here, and how well), `fetch`
(verified, resumable downloads), `engine` (the pinned llama.cpp build), and
`cmd/wasp-look` (see what the wizard will offer: `go run ./cmd/wasp-look examples/catalog.json`).

Phase 2 packages: `wizard` (a loopback setup window any product fills in: a
`Product` of pages and stages, a token-guarded page app, an event-driven
long-polled step runner, a non-interactive mode), `service` (systemd user
units, launchd plists and Windows scheduled tasks behind an injectable
runner) and `runtime` (a private Python from python-build-standalone). See
`examples/hello-setup` for the smallest product (`go run ./examples/hello-setup`).
