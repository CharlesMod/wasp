package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CharlesMod/wasp/profile"
	"github.com/CharlesMod/wasp/wizard"
)

// The kit end to end with no browser: serve, GET the page, answer, follow the
// run to done, close.
func TestHelloOverHTTP(t *testing.T) {
	s, err := wizard.New(Hello{}, wizard.Options{NoWindow: true, Root: t.TempDir(), Profile: &profile.Profile{OS: "linux", CPUs: 8}})
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- s.Serve(context.Background()) }()

	u, _ := url.Parse(s.URL())
	if u.Hostname() != "127.0.0.1" || u.Query().Get("t") == "" {
		t.Fatalf("url: %s", s.URL())
	}
	call := func(method, path string, body any) (int, []byte) {
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, "http://"+s.Addr()+path, rd)
		req.Header.Set(wizard.TokenHeader, s.Token())
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}

	// the page, as the window gets it from the URL
	resp, err := http.Get(s.URL())
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	for _, want := range []string{"Hello Setup", `data-page="welcome"`, `data-page="who"`, `data-page="done"`, "--accent:#4cc9f0"} {
		if !strings.Contains(string(page), want) {
			t.Errorf("page lacks %q", want)
		}
	}

	// page two's data, then a refused check, then an accepted one
	_, b := call("GET", "/api/page/who", nil)
	if !strings.Contains(string(b), `"cpus":8`) {
		t.Errorf("data: %s", b)
	}
	_, b = call("POST", "/api/check", map[string]any{"page": "who", "answers": map[string]any{"name": " "}})
	if !strings.Contains(string(b), "Please type a name") {
		t.Errorf("check: %s", b)
	}
	_, b = call("POST", "/api/check", map[string]any{"page": "who", "answers": map[string]any{"name": "Ada"}})
	if !strings.Contains(string(b), `"ok":true`) {
		t.Fatalf("check: %s", b)
	}

	if c, b := call("POST", "/api/start", map[string]int{}); c != 202 {
		t.Fatalf("start: %d %s", c, b)
	}
	seen, state := 0, ""
	var says []string
	var stages []string
	sawProgress := false
	deadline := time.Now().Add(10 * time.Second)
	for state != "done" && time.Now().Before(deadline) {
		_, b := call("GET", "/api/run?seen="+strconv.Itoa(seen), nil)
		var sn wizard.Snapshot
		if err := json.Unmarshal(b, &sn); err != nil {
			t.Fatal(err)
		}
		for _, e := range sn.Events {
			seen = max(seen, e.Seq)
			switch e.Kind {
			case wizard.EvSay:
				says = append(says, e.Text)
			case wizard.EvStage:
				stages = append(stages, e.Text)
			case wizard.EvProgress:
				sawProgress = true
			}
		}
		seen = max(seen, sn.Seq)
		state = sn.State
		if state == "failed" {
			t.Fatalf("run failed: %s", sn.Error)
		}
	}
	if state != "done" {
		t.Fatal("run did not reach done")
	}
	if strings.Join(stages, "|") != "Saying hello|Saying goodbye" {
		t.Errorf("stages: %v", stages)
	}
	if strings.Join(says, "|") != "Hello, Ada.|Goodbye, Ada." {
		t.Errorf("says: %v", says)
	}
	if !sawProgress {
		t.Error("no progress seen")
	}

	call("POST", "/api/close", map[string]int{})
	select {
	case err := <-served:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop")
	}
}

func TestCommandNonInteractive(t *testing.T) {
	dir := t.TempDir()
	af := filepath.Join(dir, "a.json")
	os.WriteFile(af, []byte(`{"name":"Grace"}`), 0o644)
	var out, errb bytes.Buffer
	rc := wizard.Command(Hello{}, []string{"--yes", "--answers", af, "--root", dir}, &out, &errb)
	if rc != 0 {
		t.Fatalf("rc %d: %s", rc, errb.String())
	}
	if !regexp.MustCompile(`==> Saying hello\n\s+Hello, Grace\.\n==> Saying goodbye`).MatchString(out.String()) {
		t.Errorf("out: %q", out.String())
	}
	if rc := wizard.Command(Hello{}, []string{"--yes", "--root", dir}, &out, &errb); rc != 1 {
		t.Errorf("no name should fail, rc %d", rc)
	}
}
