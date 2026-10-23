package main

// Launches an AI (an official CLI, or Perplexity's HTTP API), feeds it a
// prompt, and collects its answer.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

var (
	limitRe = regexp.MustCompile(`(?i)usage limit|rate.?limit|quota|limit (?:reached|exceeded)|too many requests|\b429\b|resource.?exhausted|out of (?:credits|messages)|insufficient (?:balance|credits?|funds)|\b402\b|payment required`)
	authRe  = regexp.MustCompile(`(?i)not (?:logged|signed) in|please (?:log|sign) ?in|unauthori[sz]ed|authentication (?:required|failed)|invalid api key|\b401\b|run .*login`)
)

type Result struct {
	OK           bool
	Output       string
	Stderr       string
	Code         int
	Limited      bool
	AuthProblem  bool
	NotInstalled bool
	KeyMissing   bool
	Cancelled    bool
	TimedOut     bool
	Ms           int64
}

type RunOpts struct {
	Prompt  string
	Mode    string // "read" or "write"
	Cwd     string
	Timeout time.Duration
	OnChunk func(text, stream string)
}

var promptCounter atomic.Int64

// PreparePrompt builds the argument list, and the stdin text when the CLI reads
// the prompt from stdin. Prompts that are too long for an argument go into a file.
func PreparePrompt(a *Agent, mode, prompt, cwd string, forceFile bool) ([]string, *string, error) {
	tmpl, ok := a.Modes[mode]
	if !ok {
		return nil, nil, fmt.Errorf("%s has no %q mode configured", a.Label, mode)
	}
	usesArg := false
	for _, t := range tmpl {
		if t == "{prompt}" {
			usesArg = true
		}
	}
	text := prompt
	if usesArg && (forceFile || (a.MaxArgPrompt > 0 && len(prompt) > a.MaxArgPrompt)) {
		dir := filepath.Join(cwd, ".ensemble", "prompts")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, nil, err
		}
		file := filepath.Join(dir, fmt.Sprintf("prompt-%d-%d.md", time.Now().UnixMilli(), promptCounter.Add(1)))
		if err := os.WriteFile(file, []byte(prompt), 0o644); err != nil {
			return nil, nil, err
		}
		rel, _ := filepath.Rel(cwd, file)
		text = "Your full instructions are in the file " + filepath.ToSlash(rel) +
			" (relative to the current folder). Read that whole file first, then do exactly what it says. Reply only with what it asks for."
	}
	args := make([]string, len(tmpl))
	for i, t := range tmpl {
		if t == "{prompt}" {
			args[i] = text
		} else {
			args[i] = t
		}
	}
	if usesArg {
		return args, nil, nil
	}
	return args, &text, nil
}

// AgentEnv is the environment for a CLI agent, including any API key it needs.
func AgentEnv(a *Agent) []string {
	env := map[string]string{}
	order := []string{}
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		if _, seen := env[k]; !seen {
			order = append(order, k)
		}
		env[k] = v
	}
	set := func(k, v string) {
		if _, seen := env[k]; !seen {
			order = append(order, k)
		}
		env[k] = v
	}
	set("NO_COLOR", "1")
	set("FORCE_COLOR", "0")
	for _, k := range a.UnsetEnv {
		delete(env, k)
	}
	key := ""
	if a.KeyName != "" {
		key = GetKey(a.KeyName)
	}
	for k, v := range a.Env {
		v = strings.ReplaceAll(v, "{key}", key)
		v = strings.ReplaceAll(v, "{home}", filepath.ToSlash(HomeDir()))
		set(k, v)
	}
	out := make([]string, 0, len(env))
	for _, k := range order {
		if v, ok := env[k]; ok {
			out = append(out, k+"="+v)
		}
	}
	return out
}

func RunAgent(ctx context.Context, a *Agent, o RunOpts) Result {
	if o.OnChunk == nil {
		o.OnChunk = func(string, string) {}
	}
	if o.Mode == "" {
		o.Mode = "read"
	}
	if a.KeyName != "" && GetKey(a.KeyName) == "" {
		return Result{Code: -1, KeyMissing: true}
	}
	if a.Type == "perplexity" {
		return runPerplexity(ctx, a, o)
	}
	return runCLIAgent(ctx, a, o)
}

// chunkWriter collects output and forwards whole UTF-8 characters as they arrive.
type chunkWriter struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	pending []byte
	stream  string
	onChunk func(string, string)
}

func (w *chunkWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p)
	data := append(w.pending, p...)
	cut := len(data)
	for i := 0; i < utf8.UTFMax && cut > 0; i++ {
		if utf8.Valid(data[:cut]) {
			break
		}
		cut--
	}
	if !utf8.Valid(data[:cut]) {
		cut = len(data) // genuinely invalid bytes; pass them through
	}
	if cut > 0 {
		w.onChunk(string(data[:cut]), w.stream)
	}
	w.pending = append([]byte(nil), data[cut:]...)
	return len(p), nil
}

func (w *chunkWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.ToValidUTF8(w.buf.String(), "�")
}

func runCLIAgent(ctx context.Context, a *Agent, o RunOpts) Result {
	start := time.Now()
	resolved := ResolveCommand(a.Command)
	// cmd.exe cuts arguments at line breaks, so .cmd/.bat launchers always get a file.
	forceFile := IsBatchFile(resolved) && strings.ContainsAny(o.Prompt, "\r\n")
	args, stdin, err := PreparePrompt(a, o.Mode, o.Prompt, o.Cwd, forceFile)
	if err != nil {
		return Result{Code: -1, Stderr: err.Error()}
	}
	cmd := NewCommand(resolved, args)
	cmd.Dir = o.Cwd
	cmd.Env = AgentEnv(a)
	if stdin != nil {
		cmd.Stdin = strings.NewReader(*stdin)
	}
	stdout := &chunkWriter{stream: "stdout", onChunk: o.OnChunk}
	stderr := &chunkWriter{stream: "stderr", onChunk: o.OnChunk}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = 5 * time.Second
	PrepareCmd(cmd)

	if err := cmd.Start(); err != nil {
		return Result{Code: -1, Stderr: err.Error(), NotInstalled: isNotFound(err), Ms: time.Since(start).Milliseconds()}
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	timeout := o.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Minute
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var waitErr error
	var cancelled, timedOut bool
	select {
	case waitErr = <-done:
	case <-ctx.Done():
		cancelled = true
		KillTree(cmd)
		waitErr = <-done
	case <-timer.C:
		timedOut = true
		KillTree(cmd)
		waitErr = <-done
	}
	code := -1
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	out := strings.TrimSpace(stdout.String())
	errText := strings.TrimSpace(stderr.String())
	ok := waitErr == nil && code == 0 && !cancelled && !timedOut && out != ""
	both := out + "\n" + errText
	return Result{
		OK:          ok,
		Output:      out,
		Stderr:      errText,
		Code:        code,
		Limited:     !ok && limitRe.MatchString(both),
		AuthProblem: !ok && authRe.MatchString(both),
		Cancelled:   cancelled,
		TimedOut:    timedOut,
		Ms:          time.Since(start).Milliseconds(),
	}
}

func isNotFound(err error) bool {
	return errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist)
}

// runPerplexity calls Perplexity's Agent API (web search with sources). It
// answers only; it never edits files.
func runPerplexity(ctx context.Context, a *Agent, o RunOpts) Result {
	start := time.Now()
	if o.Mode == "write" {
		return Result{Code: -1, Stderr: "Perplexity cannot edit files."}
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Minute
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	body := map[string]any{"input": o.Prompt}
	req := a.Request
	if req == nil {
		req = map[string]any{"preset": "low"}
	}
	for k, v := range req {
		body[k] = v
	}
	b, _ := json.Marshal(body)
	endpoint := a.Endpoint
	if endpoint == "" {
		endpoint = "https://api.perplexity.ai/v1/agent"
	}
	hreq, err := http.NewRequestWithContext(cctx, http.MethodPost, endpoint, bytes.NewReader(b))
	if err != nil {
		return Result{Code: -1, Stderr: err.Error()}
	}
	hreq.Header.Set("Authorization", "Bearer "+GetKey(a.KeyName))
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(hreq)
	if err != nil {
		r := Result{Code: -1, Stderr: err.Error(), Ms: time.Since(start).Milliseconds()}
		if ctx.Err() != nil {
			r.Cancelled = true
		} else if cctx.Err() != nil {
			r.TimedOut = true
		}
		return r
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		s := string(raw)
		if len(s) > 300 {
			s = s[:300]
		}
		return Result{
			Code:        resp.StatusCode,
			Stderr:      fmt.Sprintf("HTTP %d: %s", resp.StatusCode, s),
			Limited:     resp.StatusCode == 402 || resp.StatusCode == 429 || limitRe.Match(raw),
			AuthProblem: resp.StatusCode == 401 || resp.StatusCode == 403,
			Ms:          time.Since(start).Milliseconds(),
		}
	}
	var js map[string]any
	if err := json.Unmarshal(raw, &js); err != nil {
		return Result{Code: -1, Stderr: "Perplexity sent a reply Ensemble couldn't read: " + err.Error()}
	}
	out := FormatPerplexity(js)
	o.OnChunk(out, "stdout")
	note := ""
	if usage, ok := js["usage"].(map[string]any); ok {
		if cost, ok := usage["cost"].(map[string]any); ok {
			if tc, ok := cost["total_cost"].(float64); ok {
				note = fmt.Sprintf("cost: $%.4f", tc)
			}
		}
	}
	return Result{OK: out != "", Output: out, Stderr: note, Ms: time.Since(start).Milliseconds()}
}

// FormatPerplexity pulls the answer text and a source list out of an Agent API reply.
func FormatPerplexity(js map[string]any) string {
	items, _ := js["output"].([]any)
	text, _ := js["output_text"].(string)
	if text == "" {
		parts := []string{}
		for _, it := range items {
			m, _ := it.(map[string]any)
			if m["type"] != "message" {
				continue
			}
			content, _ := m["content"].([]any)
			for _, c := range content {
				cm, _ := c.(map[string]any)
				if t, ok := cm["text"].(string); ok {
					parts = append(parts, t)
				}
			}
		}
		text = strings.Join(parts, "\n")
	}
	text = strings.TrimSpace(text)
	sources := []string{}
	seen := map[string]bool{}
	for _, it := range items {
		m, _ := it.(map[string]any)
		if m["type"] != "search_results" {
			continue
		}
		results, _ := m["results"].([]any)
		for _, r := range results {
			rm, _ := r.(map[string]any)
			u, _ := rm["url"].(string)
			if u == "" || seen[u] {
				continue
			}
			seen[u] = true
			title, _ := rm["title"].(string)
			if title == "" {
				title = u
			}
			title = strings.NewReplacer("[", "", "]", "").Replace(title)
			prefix := ""
			if id, ok := rm["id"]; ok && id != nil {
				prefix = fmt.Sprintf("[%v] ", id)
			}
			sources = append(sources, fmt.Sprintf("- %s[%s](%s)", prefix, title, u))
		}
	}
	if text != "" && len(sources) > 0 {
		if len(sources) > 15 {
			sources = sources[:15]
		}
		return text + "\n\n**Sources**\n" + strings.Join(sources, "\n")
	}
	return text
}

type AgentStatus struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	Membership string `json:"membership,omitempty"`
	Install    string `json:"install,omitempty"`
	Login      string `json:"login,omitempty"`
	KeyName    string `json:"keyName,omitempty"`
	Status     string `json:"status"` // ready, missing, needs-key, disabled
	Version    string `json:"version,omitempty"`
	Detail     string `json:"detail,omitempty"`
	CanWrite   bool   `json:"canWrite"`
	NoFiles    bool   `json:"noFiles,omitempty"`
	CanInstall bool   `json:"canInstall,omitempty"` // one-click install available
	CanSignIn  bool   `json:"canSignIn,omitempty"`
}

// CheckAgent is the quick health check used by `doctor` and the dashboard.
func CheckAgent(id string, a *Agent) AgentStatus {
	s := AgentStatus{ID: id, Label: a.Label, Membership: a.Membership, Install: a.Install, Login: a.Login, KeyName: a.KeyName, NoFiles: a.NoFiles,
		CanInstall: a.InstallScript != "" && InstallSupported(), CanSignIn: a.LoginCmd != "" && InstallSupported()}
	if !a.IsEnabled() {
		s.Status = "disabled"
		return s
	}
	hasKey := a.KeyName == "" || GetKey(a.KeyName) != ""
	if a.Type == "perplexity" {
		if hasKey {
			s.Status, s.Version = "ready", "Agent API"
		} else {
			s.Status, s.Detail = "needs-key", "no API key yet"
		}
		return s
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := NewCommand(ResolveCommand(a.Command), a.VersionArgs)
	cmd.Env = AgentEnv(a)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.WaitDelay = 2 * time.Second
	PrepareCmd(cmd)
	err := cmd.Start()
	if err == nil {
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err = <-done:
		case <-ctx.Done():
			KillTree(cmd)
			<-done
			err = errors.New("timed out")
		}
	}
	first := strings.TrimSpace(strings.SplitN(strings.TrimSpace(out.String()), "\n", 2)[0])
	if len(first) > 80 {
		first = first[:80]
	}
	switch {
	case err == nil && !hasKey:
		s.Status, s.Detail, s.Version = "needs-key", "no API key yet", first
	case err == nil:
		s.Status, s.Version, s.CanWrite = "ready", first, a.CanWrite()
	case isNotFound(err):
		s.Status, s.Detail = "missing", "not installed"
	default:
		d := strings.TrimSpace(out.String())
		if d == "" {
			d = err.Error()
		}
		if len(d) > 200 {
			d = d[:200]
		}
		s.Status, s.Detail = "missing", d
	}
	return s
}

func DescribeFailure(label string, r Result) string {
	switch {
	case r.Cancelled:
		return label + " was cancelled."
	case r.NotInstalled:
		return label + " isn't installed."
	case r.KeyMissing:
		return label + " has no API key yet."
	case r.TimedOut:
		return label + " timed out."
	case r.Limited:
		return label + " hit its usage limit or ran out of credit."
	case r.AuthProblem:
		return label + " isn't signed in."
	}
	tail := r.Stderr
	if tail == "" {
		tail = r.Output
	}
	lines := []string{}
	for _, l := range strings.Split(tail, "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, strings.TrimSpace(l))
		}
	}
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	t := strings.Join(lines, " | ")
	if len(t) > 300 {
		t = t[:300]
	}
	if t != "" {
		return fmt.Sprintf("%s failed (exit %d): %s.", label, r.Code, t)
	}
	return fmt.Sprintf("%s failed (exit %d).", label, r.Code)
}
