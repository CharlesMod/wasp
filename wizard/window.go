package wizard

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// WindowCommand is the command that opens url in a window of its own: Edge
// in app mode on Windows (edge is its path, "" if none), `open` on macOS,
// xdg-open on Linux. ok is false when the platform has none.
func WindowCommand(goos, edge, url string, w, h int) (name string, args []string, ok bool) {
	if w <= 0 {
		w = 920
	}
	if h <= 0 {
		h = 760
	}
	switch goos {
	case "windows":
		if edge == "" {
			return "", nil, false
		}
		return edge, []string{"--app=" + url, fmt.Sprintf("--window-size=%d,%d", w, h)}, true
	case "darwin":
		return "open", []string{url}, true
	case "linux":
		return "xdg-open", []string{url}, true
	}
	return "", nil, false
}

// EdgePath finds msedge.exe in its usual places.
func EdgePath() string {
	for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles", "LOCALAPPDATA"} {
		base := os.Getenv(env)
		if base == "" {
			continue
		}
		p := filepath.Join(base, "Microsoft", "Edge", "Application", "msedge.exe")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// OpenWindow opens url in a window; an error means the caller should print
// the address instead.
func OpenWindow(url string, w, h int) error {
	edge := ""
	if runtime.GOOS == "windows" {
		edge = EdgePath()
	}
	name, args, ok := WindowCommand(runtime.GOOS, edge, url, w, h)
	if !ok {
		return fmt.Errorf("no way to open a window on %s", runtime.GOOS)
	}
	return exec.Command(name, args...).Start()
}
