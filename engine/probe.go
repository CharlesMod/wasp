package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// Env is what llama-server needs in its environment to find the libraries
// shipped beside it (the release archives put libggml*, libllama* next to the
// binary). Windows finds DLLs beside the program by itself.
func Env(server, goos string) map[string]string {
	switch goos {
	case "linux":
		return map[string]string{"LD_LIBRARY_PATH": filepath.Dir(server)}
	case "darwin":
		return map[string]string{"DYLD_LIBRARY_PATH": filepath.Dir(server)}
	}
	return map[string]string{}
}

// missingLib is the dynamic loader's sentence for a library the machine lacks.
var missingLib = regexp.MustCompile(`error while loading shared libraries: ([^:\s]+)`)

// libPackages names the package that brings a library on the common
// distributions, for the sentence a person reads.
var libPackages = map[string]string{
	"libgomp.so.1":   "libgomp1 on Debian and Ubuntu, libgomp on Fedora",
	"libvulkan.so.1": "libvulkan1 on Debian and Ubuntu, vulkan-loader on Fedora",
	"libcurl.so.4":   "libcurl4 on Debian and Ubuntu, libcurl on Fedora",
}

// Probe runs the engine once (`llama-server --version`) the way it will be
// served, so an engine that cannot start on this machine is said at install
// time, naming what it lacks, not later as a server that keeps exiting. It
// probes only an engine built for the running system.
func Probe(ctx context.Context, server string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, server, "--version")
	cmd.Env = os.Environ()
	for k, v := range Env(server, runtime.GOOS) {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	return probeError(string(out), err)
}

func probeError(out string, err error) error {
	if m := missingLib.FindStringSubmatch(out); m != nil {
		if pkg, ok := libPackages[m[1]]; ok {
			return fmt.Errorf("the model server needs %s, which this machine lacks: install %s, then Retry", m[1], pkg)
		}
		return fmt.Errorf("the model server needs %s, which this machine lacks: install the package that provides it, then Retry", m[1])
	}
	last := strings.TrimSpace(out)
	if i := strings.LastIndexByte(last, '\n'); i >= 0 {
		last = last[i+1:]
	}
	if last == "" {
		return fmt.Errorf("the model server does not start here: %v", err)
	}
	return fmt.Errorf("the model server does not start here: %v: %s", err, last)
}
