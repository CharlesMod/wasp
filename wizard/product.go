// Package wizard is a setup window any product fills in: a loopback HTTP
// server (127.0.0.1, a free port, a random token) that serves one page app
// and an API, opened in a window of its own, and gone when the product says
// it is done.
package wizard

import (
	"context"
	"fmt"
	"sort"

	"github.com/CharlesMod/wasp/profile"
)

// Product is what a setup is made of.
type Product interface {
	Info() Info
	// Pages are in order. The first is the product's Welcome, the last its
	// Done; the kit puts its own Installing page between the last two.
	Pages() []Page
	// Plan gives the stages for what the person chose.
	Plan(answers Answers) ([]Stage, error)
}

// Info names the product and sets its look.
type Info struct {
	Name     string
	Version  string
	Accent   string // CSS colour; "" keeps the palette's
	AccentBG string
	OnAccent string
	LogoPNG  []byte // optional
	WindowW  int    // default 920
	WindowH  int    // default 760
}

// Page is one screen of the wizard.
type Page struct {
	ID, Title, Lede string
	// HTML is the page body, a fragment; the ids it uses are its own.
	HTML string
	// Data is handed to the page's script on entry (the profile, a fit table).
	Data func(Context) (any, error)
	// Check validates the page's answers before Next.
	Check func(Context, Answers) error
	// Script is the page's own small JS. It may declare
	// render(data) and collect() -> answers; `root` is the page's element.
	Script string
}

// Stage is one step of the install.
type Stage struct {
	Name string // what the step list says ("Downloading the mind")
	Run  func(ctx context.Context, s *Step) error
}

// Context is what a page's Data and Check, and a product, can see.
type Context struct {
	Profile profile.Profile
	Root    string
	Answers Answers
}

// Answers are what the person chose, merged across pages.
type Answers map[string]any

// String is the answer under key as text, or "".
func (a Answers) String(key string) string {
	if v, ok := a[key]; ok && v != nil {
		if s, ok := v.(string); ok {
			return s
		}
		return fmt.Sprint(v)
	}
	return ""
}

// Bool is the answer under key as a boolean.
func (a Answers) Bool(key string) bool {
	switch v := a[key].(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "1" || v == "yes"
	}
	return false
}

// Strings is the answer under key as a list of text.
func (a Answers) Strings(key string) []string {
	switch v := a[key].(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, x := range v {
			out = append(out, fmt.Sprint(x))
		}
		return out
	}
	return nil
}

// Keys are the answered keys, sorted.
func (a Answers) Keys() []string {
	k := make([]string, 0, len(a))
	for key := range a {
		k = append(k, key)
	}
	sort.Strings(k)
	return k
}

func (a Answers) clone() Answers {
	out := make(Answers, len(a))
	for k, v := range a {
		out[k] = v
	}
	return out
}

// Step is a stage's way to speak to the person.
type Step struct {
	say  func(string)
	prog func(done, total int64)
}

// Say appends a log line.
func (s *Step) Say(line string) {
	if s != nil && s.say != nil {
		s.say(line)
	}
}

// Sayf is Say with a format.
func (s *Step) Sayf(format string, a ...any) { s.Say(fmt.Sprintf(format, a...)) }

// Progress shows a bar; total is -1 when unknown.
func (s *Step) Progress(done, total int64) {
	if s != nil && s.prog != nil {
		s.prog(done, total)
	}
}
