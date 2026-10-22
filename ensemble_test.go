package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestMain lets the test binary double as a fake AI CLI: when Ensemble
// launches it with ENSEMBLE_AS_MOCK=1, it behaves like a tiny agent.
func TestMain(m *testing.M) {
	if os.Getenv("ENSEMBLE_AS_MOCK") == "1" {
		mockAgent(os.Args[1:])
		return
	}
	exe, _ := os.Executable()
	os.Setenv("ENSEMBLE_MOCK", exe)
	home, _ := os.MkdirTemp("", "ens-home-")
	os.Setenv("ENSEMBLE_HOME", home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

func mockAgent(args []string) {
	id, mode := args[0], args[1]
	if mode == "--version" {
		fmt.Printf("mock-%s 1.0.0\n", id)
		os.Exit(0)
	}
	b, _ := io.ReadAll(os.Stdin)
	prompt := string(b)
	switch os.Getenv("MOCK_FAIL_" + strings.ToUpper(id)) {
	case "limit":
		fmt.Fprintln(os.Stderr, "Error: You have reached your usage limit. Try again later.")
		os.Exit(1)
	case "crash":
		fmt.Fprintln(os.Stderr, "boom")
		os.Exit(2)
	case "hang":
		time.Sleep(time.Hour)
	}
	switch {
	case strings.Contains(prompt, "You are the researcher"):
		fmt.Println("- Use the current stable API. [1]\n\n**Sources**\n- [1] [Docs](https://example.com/docs)")
	case strings.Contains(prompt, "lead planner"):
		if strings.Contains(prompt, "WEB RESEARCH") {
			os.WriteFile(filepath.Join(".ensemble", "planner-saw-research"), []byte("1"), 0o644)
		}
		fmt.Println("## Summary\nCreate hello.txt.\n## Steps\n1. Write hello.txt\n## Acceptance criteria\n- [ ] hello.txt says hello and is marked fixed\n## Risks\nNone.")
	case strings.Contains(prompt, "You are the builder"):
		if mode != "write" {
			fmt.Fprintln(os.Stderr, "not allowed to write")
			os.Exit(3)
		}
		os.WriteFile("hello.txt", []byte("hello from "+id+"\n"), 0o644)
		fmt.Printf("Created hello.txt (%s).\n", id)
	case strings.Contains(prompt, "code reviewer"):
		b, _ := os.ReadFile("hello.txt")
		if strings.Contains(string(b), "fixed") {
			fmt.Println("## Findings\nNone.\n## Verdict\nVERDICT: APPROVED")
		} else {
			fmt.Println("## Findings\n1. hello.txt is not marked fixed.\n## Verdict\nVERDICT: CHANGES_REQUESTED")
		}
	case strings.Contains(prompt, "You are fixing code"):
		f, _ := os.OpenFile("hello.txt", os.O_APPEND|os.O_WRONLY, 0o644)
		f.WriteString("fixed by " + id + "\n")
		f.Close()
		fmt.Println("Marked hello.txt as fixed.")
	case strings.Contains(prompt, "chairing a panel"):
		n := len(regexp.MustCompile(`(?m)^### `).FindAllString(prompt, -1))
		fmt.Printf("Final merged answer from %d panel answers.\n\n## Where the models disagreed\nNone of note.\n", n)
	case strings.Contains(prompt, "Revised answer"):
		fmt.Printf("## Critique\nFine.\n## Revised answer\n%s revised answer.\n", id)
	default:
		fmt.Printf("%s first answer. ünïcödé ✓\n", id)
	}
	os.Exit(0)
}

func waitRun(t *testing.T, r *Run) {
	t.Helper()
	select {
	case <-r.Done():
	case <-time.After(60 * time.Second):
		t.Fatal("run did not finish")
	}
	if r.Status() != "done" {
		t.Fatalf("run status %s: %s", r.Status(), r.LastError())
	}
}

func steps(r *Run) []string {
	out := []string{}
	for _, e := range r.EventsFrom(0) {
		if e.Type == "step-start" {
			out = append(out, e.Title+":"+e.Agent)
		}
	}
	return out
}

func notes(r *Run) string {
	var b strings.Builder
	for _, e := range r.EventsFrom(0) {
		if e.Type == "note" {
			b.WriteString(e.Text + "\n")
		}
	}
	return b.String()
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, b)
	}
	return strings.TrimSpace(string(b))
}

func TestDoctorSeesAllFive(t *testing.T) {
	list, err := AgentStatuses(true)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, s := range list {
		got = append(got, s.ID+":"+s.Status)
	}
	want := "claude:ready codex:ready gemini:ready perplexity:ready deepseek:ready"
	if strings.Join(got, " ") != want {
		t.Fatalf("got %v", got)
	}
}

func TestCouncil(t *testing.T) {
	r, err := StartCouncil(CouncilRequest{Question: "What is 2+2?", Rounds: 2})
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, r)
	if n := len(steps(r)); n != 11 { // 5 answers + 5 revisions + 1 synthesis
		t.Fatalf("expected 11 steps, got %d: %v", n, steps(r))
	}
	if !strings.Contains(r.Result().Markdown, "Final merged answer from 5 panel answers") {
		t.Fatal(r.Result().Markdown)
	}
	if _, err := os.Stat(filepath.Join(r.RunDir, "final.md")); err != nil {
		t.Fatal(err)
	}
	// Streamed output keeps multi-byte characters intact.
	for _, e := range r.EventsFrom(0) {
		if e.Type == "step-end" && strings.Contains(e.Output, "first answer") && !strings.Contains(e.Output, "ünïcödé ✓") {
			t.Fatalf("unicode mangled: %q", e.Output)
		}
	}
}

func TestBuildFullLoop(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# demo\n"), 0o644)
	r, err := StartBuild(BuildRequest{Dir: dir, Task: "Create hello.txt"})
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, r)
	want := "Research:perplexity Plan:claude Build:codex Review 1:gemini Fix 1:claude Review 2:gemini"
	if got := strings.Join(steps(r), " "); got != want {
		t.Fatalf("steps:\n got %s\nwant %s", got, want)
	}
	if r.Result().Verdict != "approved" {
		t.Fatal(r.Result().Verdict)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "hello.txt"))
	if string(b) != "hello from codex\nfixed by claude\n" {
		t.Fatalf("hello.txt = %q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, ".ensemble", "planner-saw-research")); err != nil {
		t.Fatal("planner did not receive the research")
	}
	if br := gitOut(t, dir, "branch", "--show-current"); !strings.HasPrefix(br, "ensemble/create-hello-txt-") {
		t.Fatal(br)
	}
	if st := gitOut(t, dir, "status", "--porcelain"); st != "" {
		t.Fatalf("work should be committed and .ensemble ignored, got %q", st)
	}
}

func TestBuildRefusesDirtyRepo(t *testing.T) {
	dir := t.TempDir()
	gitOut(t, dir, "init", "-q")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644)
	gitOut(t, dir, "add", "-A")
	gitOut(t, dir, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "init")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("changed"), 0o644)
	if _, err := StartBuild(BuildRequest{Dir: dir, Task: "x"}); err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("expected dirty refusal, got %v", err)
	}
}

func TestFallbackWhenOutOfUsage(t *testing.T) {
	t.Setenv("MOCK_FAIL_CODEX", "limit")
	dir := t.TempDir()
	one := flexInt(1)
	r, err := StartBuild(BuildRequest{Dir: dir, Task: "Create hello.txt", MaxReviewRounds: &one})
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, r)
	if !strings.Contains(notes(r), "usage limit") {
		t.Fatal(notes(r))
	}
	b, _ := os.ReadFile(filepath.Join(dir, "hello.txt"))
	if !strings.Contains(string(b), "hello from claude") {
		t.Fatalf("hello.txt = %q", b)
	}
}

func TestNoResearchAndReadOnlyRoles(t *testing.T) {
	dir := t.TempDir()
	one := flexInt(1)
	no := false
	r, err := StartBuild(BuildRequest{Dir: dir, Task: "Create hello.txt", Research: &no, MaxReviewRounds: &one,
		Roles: map[string]string{"planner": "perplexity", "builder": "perplexity", "fixer": "deepseek"}})
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, r)
	s := steps(r)
	if s[0] != "Plan:claude" || s[1] != "Build:codex" || s[3] != "Fix 1:deepseek" {
		t.Fatalf("steps %v", s)
	}
}

func TestCancelStopsAgents(t *testing.T) {
	t.Setenv("MOCK_FAIL_CLAUDE", "hang")
	r, err := StartCouncil(CouncilRequest{Question: "hi", Agents: []string{"claude"}, Rounds: 1})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	r.Cancel()
	select {
	case <-r.Done():
	case <-time.After(15 * time.Second):
		t.Fatal("cancel did not stop the run")
	}
	if r.Status() != "cancelled" {
		t.Fatalf("status %s", r.Status())
	}
}

func TestLongArgPromptsGoToFile(t *testing.T) {
	dir := t.TempDir()
	a := &Agent{Label: "G", MaxArgPrompt: 100, Modes: map[string][]string{"read": {"-p", "{prompt}"}}}
	args, stdin, _ := PreparePrompt(a, "read", "hi", dir, false)
	if stdin != nil || args[1] != "hi" {
		t.Fatal(args)
	}
	args, _, _ = PreparePrompt(a, "read", strings.Repeat("x", 500), dir, false)
	m := regexp.MustCompile(`(\.ensemble/prompts/\S+\.md)`).FindString(args[1])
	if m == "" {
		t.Fatal(args[1])
	}
	b, _ := os.ReadFile(filepath.Join(dir, m))
	if len(b) != 500 {
		t.Fatal(len(b))
	}
}

func TestPerplexityAdapter(t *testing.T) {
	var lastAuth string
	var lastBody map[string]any
	status := 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastAuth = r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&lastBody)
		if status != 200 {
			w.WriteHeader(status)
			w.Write([]byte(`{"error":"insufficient credits"}`))
			return
		}
		w.Write([]byte(`{"status":"completed","output":[
			{"type":"search_results","results":[{"id":"web:1","title":"React 20 release notes","url":"https://react.dev/blog"}]},
			{"type":"message","content":[{"type":"output_text","text":"React 20 is current [web:1]."}]}],
			"usage":{"cost":{"total_cost":0.0123}}}`))
	}))
	defer srv.Close()
	t.Setenv("PERPLEXITY_API_KEY", "pplx-test-key-123")
	a := &Agent{Label: "Perplexity", Type: "perplexity", KeyName: "perplexity", Endpoint: srv.URL, Request: map[string]any{"preset": "low"}}
	res := RunAgent(context.Background(), a, RunOpts{Prompt: "latest react?", Timeout: 5 * time.Second})
	if !res.OK || lastAuth != "Bearer pplx-test-key-123" || lastBody["input"] != "latest react?" || lastBody["preset"] != "low" {
		t.Fatalf("%+v %s %v", res, lastAuth, lastBody)
	}
	if !strings.Contains(res.Output, "[React 20 release notes](https://react.dev/blog)") {
		t.Fatal(res.Output)
	}
	if w := RunAgent(context.Background(), a, RunOpts{Prompt: "x", Mode: "write"}); w.OK {
		t.Fatal("perplexity must not write")
	}
	status = 402
	if broke := RunAgent(context.Background(), a, RunOpts{Prompt: "x", Timeout: 5 * time.Second}); broke.OK || !broke.Limited {
		t.Fatalf("%+v", broke)
	}
}

func TestKeysAndDeepSeekEnv(t *testing.T) {
	t.Setenv("ENSEMBLE_MOCK", "")
	cfg, _ := LoadConfig()
	if s := CheckAgent("perplexity", cfg.Agents["perplexity"]); s.Status != "needs-key" {
		t.Fatal(s.Status)
	}
	if r := RunAgent(context.Background(), cfg.Agents["deepseek"], RunOpts{Prompt: "hi", Cwd: t.TempDir()}); !r.KeyMissing {
		t.Fatal("expected key missing")
	}
	if err := SetKey("deepseek", "sk-deepseek-test-1234"); err != nil {
		t.Fatal(err)
	}
	defer RemoveKey("deepseek")
	if st, _ := os.Stat(keysFile()); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatalf("keys file mode %v", st.Mode().Perm())
	}
	t.Setenv("ANTHROPIC_API_KEY", "should-be-removed")
	env := strings.Join(AgentEnv(cfg.Agents["deepseek"]), "\n")
	for _, want := range []string{"ANTHROPIC_AUTH_TOKEN=sk-deepseek-test-1234", "ANTHROPIC_BASE_URL=https://api.deepseek.com/anthropic", "CLAUDE_CONFIG_DIR=" + filepath.ToSlash(HomeDir()) + "/deepseek-claude"} {
		if !strings.Contains(env, want) {
			t.Fatalf("missing %s", want)
		}
	}
	if strings.Contains(env, "ANTHROPIC_API_KEY=") {
		t.Fatal("ANTHROPIC_API_KEY should be removed for DeepSeek")
	}
	if err := SetKey("perplexity", "short"); err == nil {
		t.Fatal("expected bad key error")
	}
}

func TestConfigMergeDoesNotLeak(t *testing.T) {
	os.WriteFile(ConfigPath(), []byte(`{"agents":{"deepseek":{"env":{"ANTHROPIC_MODEL":"deepseek-v4-pro"}}},"roles":{"builder":["deepseek"]}}`), 0o600)
	defer os.Remove(ConfigPath())
	t.Setenv("ENSEMBLE_MOCK", "")
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	d := cfg.Agents["deepseek"]
	if d.Env["ANTHROPIC_MODEL"] != "deepseek-v4-pro" || d.Env["ANTHROPIC_BASE_URL"] == "" || d.Label != "DeepSeek" {
		t.Fatalf("merge wrong: %+v", d.Env)
	}
	if cfg.Roles["builder"][0] != "deepseek" || len(cfg.Roles["planner"]) == 0 {
		t.Fatal(cfg.Roles)
	}
	fresh := defaultAgents()
	if fresh["deepseek"].Env["ANTHROPIC_MODEL"] != "deepseek-flash[1m]" {
		t.Fatal("defaults were modified")
	}
}

func TestServerSecurityAndAPI(t *testing.T) {
	s, err := NewServer(0)
	if err != nil {
		t.Fatal(err)
	}
	go s.Serve()
	defer s.Close()
	base := fmt.Sprintf("http://127.0.0.1:%d", s.Port)
	get := func(path, host, tok string) (int, string) {
		req, _ := http.NewRequest("GET", base+path, nil)
		if host != "" {
			req.Host = host
		}
		if tok != "" {
			req.Header.Set("X-Ensemble-Token", tok)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := get("/", "", ""); code != 200 || !strings.Contains(body, "<title>Ensemble</title>") {
		t.Fatalf("page %d", code)
	}
	if code, _ := get("/api/agents", "", ""); code != 401 {
		t.Fatalf("no token: %d", code)
	}
	if code, _ := get("/api/agents", "evil.com", s.Token); code != 403 {
		t.Fatalf("bad host: %d", code)
	}
	code, body := get("/api/agents", "", s.Token)
	if code != 200 || !strings.Contains(body, `"deepseek"`) {
		t.Fatalf("agents %d %s", code, body)
	}

	// Start a council over HTTP and read the event stream to the end.
	req, _ := http.NewRequest("POST", base+"/api/council", strings.NewReader(`{"question":"hi","rounds":"1","agents":["claude","codex"]}`))
	req.Header.Set("X-Ensemble-Token", s.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var sum RunSummary
	json.NewDecoder(resp.Body).Decode(&sum)
	resp.Body.Close()
	if sum.ID == "" {
		t.Fatal("no run id")
	}
	_, stream := get("/api/runs/"+sum.ID+"/events?token="+s.Token, "", "")
	for _, want := range []string{`"type":"hello"`, `"type":"step-start"`, `"type":"done"`} {
		if !strings.Contains(stream, want) {
			t.Fatalf("stream missing %s:\n%s", want, stream)
		}
	}
}
