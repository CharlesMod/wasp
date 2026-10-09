package wizard

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CharlesMod/wasp/profile"
)

//go:embed ui/index.html ui/app.js ui/style.css
var assets embed.FS

// DefaultKeepalive ends a long poll that has nothing new.
const DefaultKeepalive = 25 * time.Second

// TokenHeader carries the token on every API call (or use ?t=).
const TokenHeader = "X-Wasp-Token"

// Options shapes a wizard.
type Options struct {
	Root      string           // install root; default profile.InstallRoot()
	Profile   *profile.Profile // default profile.Detect()
	NoWindow  bool             // serve only; print the URL
	Answers   Answers          // pre-filled answers (non-interactive, or a head start)
	Opener    func(url string) error
	Keepalive time.Duration // default DefaultKeepalive
	Stdout    io.Writer     // where the URL and non-interactive steps print
}

// Server is one running wizard.
type Server struct {
	p        Product
	o        Options
	ln       net.Listener
	token    string
	port     string
	prof     profile.Profile
	run      *runner
	mu       sync.Mutex
	answers  Answers
	closed   chan struct{}
	closeOne sync.Once
	baseCtx  context.Context
	baseStop context.CancelFunc
}

// New listens on a free loopback port and mints the token.
func New(p Product, o Options) (*Server, error) {
	if len(p.Pages()) < 2 {
		return nil, errors.New("wizard: a product needs at least two pages (welcome and done)")
	}
	ids := map[string]bool{}
	for _, pg := range p.Pages() {
		if pg.ID == "" || ids[pg.ID] || pg.ID == "run" {
			return nil, fmt.Errorf("wizard: page id %q is empty, repeated or reserved", pg.ID)
		}
		ids[pg.ID] = true
	}
	if o.Root == "" {
		o.Root = profile.InstallRoot()
	}
	if o.Keepalive <= 0 {
		o.Keepalive = DefaultKeepalive
	}
	prof := profile.Profile{}
	if o.Profile != nil {
		prof = *o.Profile
	} else {
		prof = profile.Detect()
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		ln.Close()
		return nil, err
	}
	ctx, stop := context.WithCancel(context.Background())
	s := &Server{p: p, o: o, ln: ln, port: port, prof: prof, run: newRunner(),
		token: hex.EncodeToString(b[:]), answers: Answers{}, closed: make(chan struct{}),
		baseCtx: ctx, baseStop: stop}
	for k, v := range o.Answers {
		s.answers[k] = v
	}
	return s, nil
}

// Token is the secret the window's URL carries.
func (s *Server) Token() string { return s.token }

// Addr is host:port.
func (s *Server) Addr() string { return "127.0.0.1:" + s.port }

// URL is the window's address, token included.
func (s *Server) URL() string { return "http://" + s.Addr() + "/?t=" + s.token }

// Closed is closed when the wizard is done.
func (s *Server) Closed() <-chan struct{} { return s.closed }

// Close ends the wizard: cancels a run and stops serving.
func (s *Server) Close() {
	s.closeOne.Do(func() {
		s.baseStop()
		s.run.Cancel()
		close(s.closed)
	})
}

// Serve answers until Close, a close call from the page, or ctx ends.
func (s *Server) Serve(ctx context.Context) error {
	hs := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		select {
		case <-ctx.Done():
			s.Close()
		case <-s.closed:
		}
		sc, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if hs.Shutdown(sc) != nil {
			hs.Close()
		}
	}()
	err := hs.Serve(s.ln)
	if errors.Is(err, http.ErrServerClosed) {
		s.run.Wait()
		return nil
	}
	return err
}

func (s *Server) wctx() Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Context{Profile: s.prof, Root: s.o.Root, Answers: s.answers.clone()}
}

// Handler is the whole surface, behind the loopback, Origin and token checks.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("GET /api/page/{id}", s.pageData)
	mux.HandleFunc("POST /api/check", s.check)
	mux.HandleFunc("POST /api/start", s.start)
	mux.HandleFunc("POST /api/retry", s.retry)
	mux.HandleFunc("GET /api/run", s.poll)
	mux.HandleFunc("POST /api/cancel", s.cancel)
	mux.HandleFunc("POST /api/close", s.closeCall)
	return s.guard(mux)
}

// hostOK accepts only the loopback address this server listens on.
func (s *Server) hostOK(h string) bool {
	switch h {
	case "127.0.0.1:" + s.port, "localhost:" + s.port, "[::1]:" + s.port:
		return true
	}
	return false
}

func (s *Server) originOK(o string) bool {
	if o == "" {
		return true // not a browser cross-origin request
	}
	u, err := url.Parse(o)
	if err != nil || u.Scheme != "http" {
		return false
	}
	return s.hostOK(u.Host)
}

func (s *Server) tokenOK(r *http.Request) bool {
	got := r.Header.Get(TokenHeader)
	if got == "" {
		got = r.URL.Query().Get("t")
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) == 1
}

func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case !s.hostOK(r.Host):
			http.Error(w, "forbidden: not the loopback address", http.StatusForbidden)
		case !s.originOK(r.Header.Get("Origin")):
			http.Error(w, "forbidden: foreign origin", http.StatusForbidden)
		case !s.tokenOK(r):
			http.Error(w, "forbidden: missing or wrong token", http.StatusForbidden)
		default:
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			next.ServeHTTP(w, r)
		}
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func jsonErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (s *Server) findPage(id string) (Page, bool) {
	for _, pg := range s.p.Pages() {
		if pg.ID == id {
			return pg, true
		}
	}
	return Page{}, false
}

func (s *Server) pageData(w http.ResponseWriter, r *http.Request) {
	pg, ok := s.findPage(r.PathValue("id"))
	if !ok {
		jsonErr(w, 404, "no such page")
		return
	}
	var data any
	if pg.Data != nil {
		d, err := pg.Data(s.wctx())
		if err != nil {
			jsonErr(w, 500, err.Error())
			return
		}
		data = d
	}
	s.mu.Lock()
	ans := s.answers.clone()
	s.mu.Unlock()
	writeJSON(w, 200, map[string]any{"data": data, "answers": ans})
}

func (s *Server) check(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Page    string  `json:"page"`
		Answers Answers `json:"answers"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		jsonErr(w, 400, "a JSON body is required")
		return
	}
	pg, ok := s.findPage(body.Page)
	if !ok {
		jsonErr(w, 404, "no such page")
		return
	}
	s.mu.Lock()
	merged := s.answers.clone()
	for k, v := range body.Answers {
		merged[k] = v
	}
	s.mu.Unlock()
	if pg.Check != nil {
		c := Context{Profile: s.prof, Root: s.o.Root, Answers: merged}
		if err := pg.Check(c, merged); err != nil {
			writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
			return
		}
	}
	s.mu.Lock()
	s.answers = merged
	s.mu.Unlock()
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) plan() ([]Stage, error) {
	s.mu.Lock()
	a := s.answers.clone()
	s.mu.Unlock()
	return s.p.Plan(a)
}

func (s *Server) start(w http.ResponseWriter, r *http.Request) {
	stages, err := s.plan()
	if err != nil {
		jsonErr(w, 400, err.Error())
		return
	}
	if err := s.run.Start(s.baseCtx, stages, 0); err != nil {
		jsonErr(w, 409, err.Error())
		return
	}
	writeJSON(w, 202, map[string]bool{"ok": true})
}

// retry re-plans and runs again from the stage that failed.
func (s *Server) retry(w http.ResponseWriter, r *http.Request) {
	from := s.run.FailedAt()
	if from < 0 {
		jsonErr(w, 409, "nothing failed to retry")
		return
	}
	stages, err := s.plan()
	if err != nil {
		jsonErr(w, 400, err.Error())
		return
	}
	if err := s.run.Start(s.baseCtx, stages, from); err != nil {
		jsonErr(w, 409, err.Error())
		return
	}
	writeJSON(w, 202, map[string]bool{"ok": true})
}

func (s *Server) poll(w http.ResponseWriter, r *http.Request) {
	seen, _ := strconv.Atoi(r.URL.Query().Get("seen"))
	writeJSON(w, 200, s.run.Poll(r.Context(), seen, s.o.Keepalive))
}

func (s *Server) cancel(w http.ResponseWriter, r *http.Request) {
	s.run.Cancel()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) closeCall(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]bool{"ok": true})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	go s.Close()
}

// bootJSON is the page list and the product's look, for the page app.
func (s *Server) bootJSON() string {
	info := s.p.Info()
	var pages []map[string]string
	for _, p := range s.p.Pages() {
		pages = append(pages, map[string]string{"id": p.ID, "title": p.Title, "lede": p.Lede})
	}
	logo := ""
	if len(info.LogoPNG) > 0 {
		logo = "data:image/png;base64," + base64.StdEncoding.EncodeToString(info.LogoPNG)
	}
	b, _ := json.Marshal(map[string]any{
		"name": info.Name, "version": info.Version, "logo": logo, "pages": pages,
		"answers": s.snapshotAnswers(),
	})
	// safe inside <script type="application/json">
	return strings.NewReplacer("<", `<`, ">", `>`, "&", `&`).Replace(string(b))
}

func (s *Server) snapshotAnswers() Answers {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.answers.clone()
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	html, _ := assets.ReadFile("ui/index.html")
	css, _ := assets.ReadFile("ui/style.css")
	js, _ := assets.ReadFile("ui/app.js")
	info := s.p.Info()
	var look strings.Builder
	if info.Accent != "" {
		look.WriteString("--accent:" + cssValue(info.Accent) + ";")
	}
	if info.AccentBG != "" {
		look.WriteString("--accent-bg:" + cssValue(info.AccentBG) + ";")
	}
	if info.OnAccent != "" {
		look.WriteString("--on-accent:" + cssValue(info.OnAccent) + ";")
	}
	style := string(css)
	if look.Len() > 0 {
		style += "\n:root,:root:root{" + look.String() + "}\n"
	}
	var sections, scripts strings.Builder
	for _, p := range s.p.Pages() {
		fmt.Fprintf(&sections, "<section class=\"page\" data-page=\"%s\" hidden>\n%s\n</section>\n", attr(p.ID), p.HTML)
		if p.Script != "" {
			fmt.Fprintf(&scripts, "PAGE_SCRIPTS[%s] = function (root) {\n%s\n;return { render: typeof render === 'function' ? render : null, collect: typeof collect === 'function' ? collect : null };\n};\n",
				jsString(p.ID), strings.ReplaceAll(p.Script, "</", `<\/`))
		}
	}
	page := strings.NewReplacer(
		"{{TITLE}}", attr(info.Name+" Setup"),
		"{{STYLE}}", strings.ReplaceAll(style, "</", `<\/`),
		"{{BOOT}}", s.bootJSON(),
		"{{SECTIONS}}", sections.String(),
		"{{PAGESCRIPTS}}", scripts.String(),
		"{{APP}}", strings.ReplaceAll(string(js), "</", `<\/`),
	).Replace(string(html))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; img-src data:; connect-src 'self'; base-uri 'none'; form-action 'none'")
	_, _ = io.WriteString(w, page)
}

func attr(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

func jsString(s string) string {
	b, _ := json.Marshal(s)
	return strings.NewReplacer("<", `<`, ">", `>`).Replace(string(b))
}

// cssValue keeps a product's colour from breaking out of its declaration.
func cssValue(s string) string {
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune(";{}<>\\\"'", r) {
			return -1
		}
		return r
	}, s)
}
