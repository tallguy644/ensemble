package main

// A Run is one council or build session. It records every step to disk,
// streams events to listeners (CLI or app window), and falls back to the next
// agent when one is missing, signed out, out of credit or out of plan usage.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Event struct {
	T       int64       `json:"t"`
	Type    string      `json:"type"`
	StepID  string      `json:"stepId,omitempty"`
	Title   string      `json:"title,omitempty"`
	Agent   string      `json:"agent,omitempty"`
	Label   string      `json:"label,omitempty"`
	Mode    string      `json:"mode,omitempty"`
	Stream  string      `json:"stream,omitempty"`
	Text    string      `json:"text,omitempty"`
	OK      *bool       `json:"ok,omitempty"`
	Ms      int64       `json:"ms,omitempty"`
	Output  string      `json:"output,omitempty"`
	Error   string      `json:"error,omitempty"`
	Name    string      `json:"name,omitempty"`
	Detail  string      `json:"detail,omitempty"`
	Level   string      `json:"level,omitempty"`
	Round   int         `json:"round,omitempty"`
	Verdict string      `json:"verdict,omitempty"`
	By      string      `json:"by,omitempty"`
	Result  *RunResult  `json:"result,omitempty"`
	Message string      `json:"message,omitempty"`
	Status  string      `json:"status,omitempty"`
	Run     *RunSummary `json:"run,omitempty"`
}

type RunResult struct {
	Markdown string `json:"markdown"`
	Verdict  string `json:"verdict,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Commit   string `json:"commit,omitempty"`
}

type RunSummary struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	StartedAt string `json:"startedAt"`
	RunDir    string `json:"runDir"`
}

type StepResult struct {
	Agent  string
	Label  string
	Output string
}

type Run struct {
	ID        string
	Kind      string
	Title     string
	Config    *Config
	RunDir    string
	Cwd       string
	StartedAt time.Time

	ctx    context.Context
	cancel context.CancelFunc
	logMu  sync.Mutex
	mu     sync.Mutex
	events []Event
	subs   map[chan struct{}]bool
	ready  map[string]bool
	gone   map[string]bool // exhausted: out of usage, signed out, or not installed
	status string
	result *RunResult
	stepNo int
	done   chan struct{}
}

func NewRunID() string {
	b := make([]byte, 2)
	_, _ = rand.Read(b)
	return time.Now().Format("20060102-150405") + "-" + hex.EncodeToString(b)
}

func NewRun(kind, title string, cfg *Config, ready []string, runDir, cwd string) (*Run, error) {
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &Run{
		ID: filepath.Base(runDir), Kind: kind, Title: title, Config: cfg, RunDir: runDir, Cwd: cwd,
		StartedAt: time.Now(), ctx: ctx, cancel: cancel, subs: map[chan struct{}]bool{},
		ready: map[string]bool{}, gone: map[string]bool{}, status: "running", done: make(chan struct{}),
	}
	for _, id := range ready {
		r.ready[id] = true
	}
	name := "Build"
	if kind == "council" {
		name = "Council"
	}
	r.writeFile("log.md", fmt.Sprintf("# %s: %s\n\nStarted %s\n\n", name, title, r.StartedAt.Format(time.RFC3339)))
	return r, nil
}

func (r *Run) writeFile(name, content string) {
	_ = os.WriteFile(filepath.Join(r.RunDir, name), []byte(content), 0o644)
}

func (r *Run) appendLog(s string) {
	r.logMu.Lock()
	defer r.logMu.Unlock()
	f, err := os.OpenFile(filepath.Join(r.RunDir, "log.md"), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err == nil {
		_, _ = f.WriteString(s)
		f.Close()
	}
}

func (r *Run) Emit(e Event) {
	e.T = time.Now().UnixMilli()
	r.mu.Lock()
	r.events = append(r.events, e)
	for ch := range r.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	r.mu.Unlock()
}

// Subscribe returns a wake-up channel; call EventsFrom to read what's new.
func (r *Run) Subscribe() chan struct{} {
	ch := make(chan struct{}, 1)
	r.mu.Lock()
	r.subs[ch] = true
	r.mu.Unlock()
	return ch
}

func (r *Run) Unsubscribe(ch chan struct{}) {
	r.mu.Lock()
	delete(r.subs, ch)
	r.mu.Unlock()
}

func (r *Run) EventsFrom(i int) []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	if i >= len(r.events) {
		return nil
	}
	return append([]Event(nil), r.events[i:]...)
}

func (r *Run) Note(text, level string) {
	if level == "" {
		level = "info"
	}
	r.appendLog("> " + text + "\n\n")
	r.Emit(Event{Type: "note", Level: level, Text: text})
}

func (r *Run) Phase(name, detail string) { r.Emit(Event{Type: "phase", Name: name, Detail: detail}) }

func (r *Run) Label(id string) string { return r.Config.Label(id) }

func (r *Run) CanUse(id, mode string) bool {
	a := r.Config.Agents[id]
	if a == nil || !a.IsEnabled() {
		return false
	}
	r.mu.Lock()
	ok := r.ready[id] && !r.gone[id]
	r.mu.Unlock()
	if !ok {
		return false
	}
	return mode != "write" || a.CanWrite()
}

func (r *Run) Cancelled() bool { return r.ctx.Err() != nil }

var errCancelled = errors.New("Run cancelled.")

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string, max int) string {
	s = strings.Trim(slugRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > max {
		s = strings.Trim(s[:max], "-")
	}
	return s
}

type StepOpts struct {
	Title      string
	Prefs      []string
	Prompt     string
	Mode       string
	NoFallback bool
}

// Step runs one unit of work, trying each preferred agent in turn.
func (r *Run) Step(o StepOpts) (StepResult, error) {
	if o.Mode == "" {
		o.Mode = "read"
	}
	order := []string{}
	seen := map[string]bool{}
	for _, id := range o.Prefs {
		if id != "" && !seen[id] && r.CanUse(id, o.Mode) {
			seen[id] = true
			order = append(order, id)
		}
	}
	if len(order) == 0 {
		extra := ""
		if o.Mode == "write" {
			extra = " (needs file-editing permission)"
		}
		return StepResult{}, fmt.Errorf("No ready agent can do %q%s.", o.Title, extra)
	}
	if o.NoFallback {
		order = order[:1]
	}
	for _, id := range order {
		if r.Cancelled() {
			return StepResult{}, errCancelled
		}
		r.mu.Lock()
		r.stepNo++
		n := r.stepNo
		r.mu.Unlock()
		stepID := fmt.Sprintf("s%d", n)
		a := r.Config.Agents[id]
		r.Emit(Event{Type: "step-start", StepID: stepID, Title: o.Title, Agent: id, Label: a.Label, Mode: o.Mode})
		res := RunAgent(r.ctx, a, RunOpts{
			Prompt:  o.Prompt,
			Mode:    o.Mode,
			Cwd:     r.Cwd,
			Timeout: time.Duration(r.Config.TimeoutMinutes) * time.Minute,
			OnChunk: func(text, stream string) {
				r.Emit(Event{Type: "chunk", StepID: stepID, Stream: stream, Text: text})
			},
		})
		out := res.Output
		if out == "" {
			out = "(none)"
		}
		file := fmt.Sprintf("%02d-%s-%s.md", n, slug(o.Title, 40), id)
		content := fmt.Sprintf("# %s (%s, %s)\n\n## Prompt\n\n%s\n\n## Output\n\n%s\n", o.Title, a.Label, o.Mode, o.Prompt, out)
		if res.Stderr != "" {
			se := res.Stderr
			if len(se) > 4000 {
				se = se[len(se)-4000:]
			}
			content += "\n## stderr\n\n```\n" + se + "\n```\n"
		}
		r.writeFile(file, content)
		if res.OK {
			r.appendLog(fmt.Sprintf("## %s (%s)\n\n%s\n\n", o.Title, a.Label, res.Output))
			r.Emit(Event{Type: "step-end", StepID: stepID, OK: boolPtr(true), Ms: res.Ms, Output: res.Output})
			return StepResult{Agent: id, Label: a.Label, Output: res.Output}, nil
		}
		why := DescribeFailure(a.Label, res)
		r.Emit(Event{Type: "step-end", StepID: stepID, OK: boolPtr(false), Ms: res.Ms, Error: why})
		if res.Cancelled || r.Cancelled() {
			return StepResult{}, errCancelled
		}
		if res.Limited || res.AuthProblem || res.NotInstalled || res.KeyMissing {
			r.mu.Lock()
			r.gone[id] = true
			r.mu.Unlock()
		}
		if !o.NoFallback {
			why += " Trying the next agent."
		}
		r.Note(why, "warn")
	}
	return StepResult{}, fmt.Errorf("Every agent failed at %q.", o.Title)
}

func (r *Run) finish(res *RunResult) {
	r.mu.Lock()
	r.status = "done"
	r.result = res
	r.mu.Unlock()
	r.writeFile("result.md", res.Markdown)
	r.appendLog("---\n\nFinished " + time.Now().Format(time.RFC3339) + "\n")
	r.Emit(Event{Type: "done", Result: res})
	close(r.done)
}

func (r *Run) fail(err error) {
	status := "failed"
	if r.Cancelled() {
		status = "cancelled"
	}
	r.mu.Lock()
	r.status = status
	r.mu.Unlock()
	r.appendLog("---\n\n" + strings.ToUpper(status) + ": " + err.Error() + "\n")
	r.Emit(Event{Type: "error", Message: err.Error(), Status: status})
	close(r.done)
}

func (r *Run) Cancel()               { r.cancel() }
func (r *Run) Done() <-chan struct{} { return r.done }
func (r *Run) Wait()                 { <-r.done }

func (r *Run) Status() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

func (r *Run) Result() *RunResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.result
}

func (r *Run) Summary() RunSummary {
	return RunSummary{ID: r.ID, Kind: r.Kind, Title: r.Title, Status: r.Status(), StartedAt: r.StartedAt.Format(time.RFC3339), RunDir: r.RunDir}
}

// LastError is the message of the final error event, if any.
func (r *Run) LastError() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.events) - 1; i >= 0; i-- {
		if r.events[i].Type == "error" {
			return r.events[i].Message
		}
	}
	return "Failed."
}
