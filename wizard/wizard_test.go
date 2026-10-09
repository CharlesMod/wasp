package wizard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CharlesMod/wasp/profile"
)

type testProduct struct {
	stages func(Answers) ([]Stage, error)
	checks int32
}

func (p *testProduct) Info() Info {
	return Info{Name: "Test", Version: "1.0", Accent: "#123456"}
}
func (p *testProduct) Pages() []Page {
	return []Page{
		{ID: "welcome", Title: "Welcome", Lede: "Hi", HTML: `<p id="w">hello</p>`},
		{ID: "name", Title: "Name", HTML: `<input type="text" id="n">`,
			Data: func(c Context) (any, error) { return map[string]any{"cpus": c.Profile.CPUs}, nil },
			Check: func(c Context, a Answers) error {
				atomic.AddInt32(&p.checks, 1)
				if a.String("name") == "" {
					return errors.New("Give it a name.")
				}
				return nil
			},
			Script: `function render(d){ root.dataset.cpus = d.cpus } function collect(){ return {name: root.querySelector('#n').value} } // </script>`},
		{ID: "done", Title: "Done", HTML: `<p>done</p>`},
	}
}
func (p *testProduct) Plan(a Answers) ([]Stage, error) { return p.stages(a) }

func start(t *testing.T, p Product, o Options) (*Server, func()) {
	t.Helper()
	pr := profile.Profile{OS: "linux", CPUs: 4}
	o.Profile, o.Root = &pr, t.TempDir()
	s, err := New(p, o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	return s, func() { cancel(); <-done }
}

func do(t *testing.T, s *Server, method, path string, body any, hdr map[string]string) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, "http://"+s.Addr()+path, rd)
	for k, v := range hdr {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func tok(s *Server) map[string]string { return map[string]string{TokenHeader: s.Token()} }

func TestSecurity(t *testing.T) {
	p := &testProduct{stages: func(Answers) ([]Stage, error) { return nil, nil }}
	s, stop := start(t, p, Options{})
	defer stop()

	if c, _ := do(t, s, "GET", "/", nil, nil); c != 403 {
		t.Errorf("no token: %d, want 403", c)
	}
	if c, _ := do(t, s, "GET", "/", nil, map[string]string{TokenHeader: "wrong"}); c != 403 {
		t.Errorf("wrong token: %d", c)
	}
	if c, _ := do(t, s, "GET", "/api/run?seen=0", nil, nil); c != 403 {
		t.Errorf("api without token: %d", c)
	}
	if c, _ := do(t, s, "POST", "/api/start", map[string]int{}, nil); c != 403 {
		t.Errorf("start without token: %d", c)
	}
	h := tok(s)
	h["Origin"] = "http://evil.example"
	if c, _ := do(t, s, "GET", "/", nil, h); c != 403 {
		t.Errorf("foreign origin: %d", c)
	}
	h["Origin"] = "null"
	if c, _ := do(t, s, "GET", "/", nil, h); c != 403 {
		t.Errorf("null origin: %d", c)
	}
	h["Origin"] = "http://127.0.0.1:1" // right host, wrong port
	if c, _ := do(t, s, "GET", "/", nil, h); c != 403 {
		t.Errorf("origin on another port: %d", c)
	}
	for _, host := range []string{"evil.example", "evil.example:" + s.port, "192.168.1.5:" + s.port, "127.0.0.1"} {
		h := tok(s)
		h["Host"] = host
		if c, _ := do(t, s, "GET", "/", nil, h); c != 403 {
			t.Errorf("host %q: %d, want 403", host, c)
		}
	}
	// the right thing works, by header and by query
	if c, _ := do(t, s, "GET", "/", nil, tok(s)); c != 200 {
		t.Errorf("good request: %d", c)
	}
	if c, _ := do(t, s, "GET", "/?t="+s.Token(), nil, map[string]string{"Origin": "http://127.0.0.1:" + s.port}); c != 200 {
		t.Errorf("good request by query with own origin: %d", c)
	}
}

func TestPageIsSelfContained(t *testing.T) {
	p := &testProduct{stages: func(Answers) ([]Stage, error) { return nil, nil }}
	s, stop := start(t, p, Options{})
	defer stop()
	c, b := do(t, s, "GET", "/", nil, tok(s))
	if c != 200 {
		t.Fatal(c)
	}
	page := string(b)
	for _, want := range []string{`data-page="welcome"`, `data-page="name"`, `data-page="done"`, `data-page="run"`,
		"--accent:#123456", "prefers-color-scheme: light", "--bg:#0a0a0a", `PAGE_SCRIPTS["name"]`, "Back", "Cancel"} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	for _, bad := range []string{"https://", "http://", "<link", "src=\"//", "{{", "</script>\n"} {
		// the only </script> allowed are the ones closing our own scripts
		if bad == "</script>\n" {
			continue
		}
		if strings.Contains(page, bad) {
			t.Errorf("page reaches outside or leaves a placeholder: %q", bad)
		}
	}
	if strings.Contains(page, "// </script>") {
		t.Error("a page script's </script> must be escaped")
	}
}

func TestPagesDataCheckAndAnswers(t *testing.T) {
	p := &testProduct{stages: func(a Answers) ([]Stage, error) {
		return []Stage{{Name: "x", Run: func(ctx context.Context, s *Step) error { s.Say("name=" + a.String("name")); return nil }}}, nil
	}}
	s, stop := start(t, p, Options{})
	defer stop()
	_, b := do(t, s, "GET", "/api/page/name", nil, tok(s))
	var pd struct{ Data map[string]any }
	json.Unmarshal(b, &pd)
	if pd.Data["cpus"].(float64) != 4 {
		t.Errorf("data: %s", b)
	}
	if c, _ := do(t, s, "GET", "/api/page/nope", nil, tok(s)); c != 404 {
		t.Error("unknown page")
	}
	_, b = do(t, s, "POST", "/api/check", map[string]any{"page": "name", "answers": map[string]any{"name": ""}}, tok(s))
	if !strings.Contains(string(b), `"ok":false`) || !strings.Contains(string(b), "Give it a name") {
		t.Errorf("check should refuse: %s", b)
	}
	_, b = do(t, s, "POST", "/api/check", map[string]any{"page": "name", "answers": map[string]any{"name": "rex"}}, tok(s))
	if !strings.Contains(string(b), `"ok":true`) {
		t.Errorf("check: %s", b)
	}
	do(t, s, "POST", "/api/start", map[string]int{}, tok(s))
	snap := follow(t, s, 5*time.Second)
	if snap.State != StateDone || !hasSay(snap, "name=rex") {
		t.Errorf("run: %+v", snap)
	}
}

func hasSay(s Snapshot, text string) bool {
	for _, e := range s.Events {
		if e.Kind == EvSay && e.Text == text {
			return true
		}
	}
	return false
}

// follow polls /api/run like the page does until the run ends.
func follow(t *testing.T, s *Server, limit time.Duration) Snapshot {
	t.Helper()
	var all Snapshot
	seen := 0
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		_, b := do(t, s, "GET", "/api/run?seen="+itoa(seen), nil, tok(s))
		var sn Snapshot
		if err := json.Unmarshal(b, &sn); err != nil {
			t.Fatal(err, string(b))
		}
		all.State, all.Error, all.Failed, all.Stages = sn.State, sn.Error, sn.Failed, sn.Stages
		for _, e := range sn.Events {
			seen = max(seen, e.Seq)
			all.Events = append(all.Events, e)
		}
		seen = max(seen, sn.Seq)
		if sn.State != StateRunning {
			return all
		}
	}
	t.Fatal("run did not end")
	return all
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

func TestPollIsAnsweredByTheEventNotATimer(t *testing.T) {
	gate := make(chan struct{})
	r := newRunner()
	if err := r.Start(context.Background(), []Stage{{Name: "s", Run: func(ctx context.Context, st *Step) error {
		<-gate
		st.Say("hello")
		<-gate
		return nil
	}}}, 0); err != nil {
		t.Fatal(err)
	}
	// wait for the stage event (seq 1) so seen=1 holds everything
	sn := r.Poll(context.Background(), 0, time.Minute)
	if len(sn.Events) != 1 || sn.Events[0].Kind != EvStage {
		t.Fatalf("first poll: %+v", sn)
	}
	got := make(chan Snapshot, 1)
	start := time.Now()
	go func() { got <- r.Poll(context.Background(), sn.Seq, time.Minute) }()
	select {
	case <-got:
		t.Fatal("poll returned with nothing new")
	case <-time.After(100 * time.Millisecond):
	}
	gate <- struct{}{} // the stage Says
	select {
	case s2 := <-got:
		if !hasSay(s2, "hello") || time.Since(start) > 5*time.Second {
			t.Fatalf("poll: %+v", s2)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("poll was not woken by the event")
	}
	gate <- struct{}{}
	r.Wait()
	if r.State() != StateDone {
		t.Fatal(r.State())
	}
}

func TestPollKeepaliveAndDisconnect(t *testing.T) {
	r := newRunner()
	block := make(chan struct{})
	r.Start(context.Background(), []Stage{{Name: "s", Run: func(ctx context.Context, st *Step) error { <-block; return nil }}}, 0)
	sn := r.Poll(context.Background(), 0, time.Minute)
	t0 := time.Now()
	k := r.Poll(context.Background(), sn.Seq, 80*time.Millisecond)
	if len(k.Events) != 0 || k.State != StateRunning || time.Since(t0) < 70*time.Millisecond {
		t.Errorf("keepalive: %+v after %v", k, time.Since(t0))
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Snapshot)
	go func() { done <- r.Poll(ctx, sn.Seq, time.Minute) }()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("a disconnected poll must return")
	}
	close(block)
	r.Wait()
}

func TestFailureRetryReplansFromTheFailedStage(t *testing.T) {
	var ran1, ran2 int32
	var fail atomic.Bool
	fail.Store(true)
	p := &testProduct{stages: func(Answers) ([]Stage, error) {
		return []Stage{
			{Name: "one", Run: func(ctx context.Context, s *Step) error { atomic.AddInt32(&ran1, 1); return nil }},
			{Name: "two", Run: func(ctx context.Context, s *Step) error {
				atomic.AddInt32(&ran2, 1)
				if fail.Load() {
					return errors.New("the disk is full")
				}
				s.Say("ok now")
				return nil
			}},
		}, nil
	}}
	s, stop := start(t, p, Options{})
	defer stop()
	do(t, s, "POST", "/api/start", map[string]int{}, tok(s))
	sn := follow(t, s, 5*time.Second)
	if sn.State != StateFailed || sn.Failed != "two" || sn.Error != "the disk is full" {
		t.Fatalf("failed run: %+v", sn)
	}
	if c, _ := do(t, s, "POST", "/api/start", map[string]int{}, tok(s)); c != 202 && c != 409 {
		t.Errorf("start after failure: %d", c)
	}
	s.run.Wait()
	fail.Store(false)
	// a fresh failure to retry from
	s.run.mu.Lock()
	s.run.state, s.run.failedAt = StateFailed, 1
	s.run.mu.Unlock()
	atomic.StoreInt32(&ran1, 0)
	if c, _ := do(t, s, "POST", "/api/retry", map[string]int{}, tok(s)); c != 202 {
		t.Fatalf("retry: %d", c)
	}
	s.run.Wait()
	if s.run.State() != StateDone || atomic.LoadInt32(&ran1) != 0 {
		t.Errorf("retry should resume at stage two: state %s, stage one ran %d", s.run.State(), ran1)
	}
}

func TestRetryWithNothingFailed(t *testing.T) {
	p := &testProduct{stages: func(Answers) ([]Stage, error) { return nil, nil }}
	s, stop := start(t, p, Options{})
	defer stop()
	if c, _ := do(t, s, "POST", "/api/retry", map[string]int{}, tok(s)); c != 409 {
		t.Errorf("retry with nothing failed: %d", c)
	}
}

func TestCancelCancelsTheStageContext(t *testing.T) {
	in := make(chan struct{})
	p := &testProduct{stages: func(Answers) ([]Stage, error) {
		return []Stage{{Name: "slow", Run: func(ctx context.Context, s *Step) error {
			close(in)
			<-ctx.Done()
			return ctx.Err()
		}}}, nil
	}}
	s, stop := start(t, p, Options{})
	defer stop()
	do(t, s, "POST", "/api/start", map[string]int{}, tok(s))
	<-in
	if c, _ := do(t, s, "POST", "/api/cancel", map[string]int{}, tok(s)); c != 200 {
		t.Fatal(c)
	}
	sn := follow(t, s, 5*time.Second)
	if sn.State != StateCanceled {
		t.Errorf("state %s", sn.State)
	}
}

func TestProgressIsCoalescedAndOrdered(t *testing.T) {
	r := newRunner()
	r.Start(context.Background(), []Stage{{Name: "dl", Run: func(ctx context.Context, s *Step) error {
		for i := int64(0); i <= 1000; i++ {
			s.Progress(i, 1000)
		}
		s.Say("after")
		return nil
	}}}, 0)
	r.Wait()
	sn := r.Snapshot(0)
	n := 0
	for i, e := range sn.Events {
		if e.Kind == EvProgress {
			n++
			if e.Done != 1000 {
				t.Errorf("only the latest progress is kept: %+v", e)
			}
		}
		if i > 0 && e.Seq < sn.Events[i-1].Seq {
			t.Errorf("events out of order: %+v", sn.Events)
		}
	}
	if n != 1 {
		t.Errorf("progress events: %d", n)
	}
}

func TestCloseEndsServe(t *testing.T) {
	p := &testProduct{stages: func(Answers) ([]Stage, error) { return nil, nil }}
	s, err := New(p, Options{Profile: &profile.Profile{}, Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve(context.Background()) }()
	if c, _ := do(t, s, "POST", "/api/close", map[string]int{}, tok(s)); c != 200 {
		t.Fatal(c)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not end after close")
	}
}

func TestNewRefusesBadPages(t *testing.T) {
	bad := &badProduct{pages: []Page{{ID: "a"}}}
	if _, err := New(bad, Options{Profile: &profile.Profile{}}); err == nil {
		t.Error("one page")
	}
	bad.pages = []Page{{ID: "a"}, {ID: "a"}}
	if _, err := New(bad, Options{Profile: &profile.Profile{}}); err == nil {
		t.Error("repeated id")
	}
	bad.pages = []Page{{ID: "a"}, {ID: "run"}}
	if _, err := New(bad, Options{Profile: &profile.Profile{}}); err == nil {
		t.Error("reserved id")
	}
}

type badProduct struct{ pages []Page }

func (b *badProduct) Info() Info                    { return Info{Name: "B"} }
func (b *badProduct) Pages() []Page                 { return b.pages }
func (b *badProduct) Plan(Answers) ([]Stage, error) { return nil, nil }

func TestNonInteractiveRun(t *testing.T) {
	p := &testProduct{stages: func(a Answers) ([]Stage, error) {
		return []Stage{
			{Name: "first", Run: func(ctx context.Context, s *Step) error { s.Say("one"); return nil }},
			{Name: "second", Run: func(ctx context.Context, s *Step) error { s.Sayf("hi %s", a.String("name")); return nil }},
		}, nil
	}}
	var out bytes.Buffer
	err := Run(context.Background(), p, Options{Stdout: &out, Answers: Answers{"name": "rex"}, Profile: &profile.Profile{}, Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	want := "==> first\n    one\n==> second\n    hi rex\n"
	if out.String() != want {
		t.Errorf("got %q want %q", out.String(), want)
	}
	// a failed check stops before any stage
	out.Reset()
	err = Run(context.Background(), p, Options{Stdout: &out, Profile: &profile.Profile{}, Root: t.TempDir()})
	if err == nil || out.Len() != 0 {
		t.Errorf("check should fail first: %v %q", err, out.String())
	}
	// a failing stage is an error naming it
	p2 := &testProduct{stages: func(Answers) ([]Stage, error) {
		return []Stage{{Name: "boom", Run: func(context.Context, *Step) error { return errors.New("nope") }}}, nil
	}}
	err = Run(context.Background(), p2, Options{Stdout: io.Discard, Answers: Answers{"name": "x"}, Profile: &profile.Profile{}, Root: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v", err)
	}
}

func TestWindowCommand(t *testing.T) {
	n, a, ok := WindowCommand("windows", `C:\e\msedge.exe`, "http://127.0.0.1:1/?t=x", 0, 0)
	if !ok || n != `C:\e\msedge.exe` || a[0] != "--app=http://127.0.0.1:1/?t=x" || a[1] != "--window-size=920,760" {
		t.Errorf("windows: %v %v", n, a)
	}
	if _, _, ok := WindowCommand("windows", "", "u", 0, 0); ok {
		t.Error("no edge, no window")
	}
	if n, a, _ := WindowCommand("darwin", "", "u", 0, 0); n != "open" || a[0] != "u" {
		t.Error("darwin")
	}
	if n, _, _ := WindowCommand("linux", "", "u", 0, 0); n != "xdg-open" {
		t.Error("linux")
	}
	if _, _, ok := WindowCommand("plan9", "", "u", 0, 0); ok {
		t.Error("plan9")
	}
}

func TestAnswersHelpers(t *testing.T) {
	a := Answers{"s": "x", "n": 3.0, "b": true, "l": []any{"a", "b"}}
	if a.String("s") != "x" || a.String("n") != "3" || a.String("zz") != "" || !a.Bool("b") || len(a.Strings("l")) != 2 {
		t.Error("helpers")
	}
	if strings.Join(a.Keys(), ",") != "b,l,n,s" {
		t.Error("keys")
	}
}
