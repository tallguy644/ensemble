package main

// Entry points shared by the command line and the app window.

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	runsMu   sync.Mutex
	runs     = map[string]*Run{}
	runOrder []string

	statusMu    sync.Mutex
	statusCache []AgentStatus
	statusAt    time.Time
)

func ClearStatusCache() {
	statusMu.Lock()
	statusCache = nil
	statusMu.Unlock()
}

// AgentStatuses checks every agent in parallel (cached for a minute).
func AgentStatuses(refresh bool) ([]AgentStatus, error) {
	statusMu.Lock()
	if !refresh && statusCache != nil && time.Since(statusAt) < time.Minute {
		out := statusCache
		statusMu.Unlock()
		return out, nil
	}
	statusMu.Unlock()
	cfg, err := LoadConfig()
	if err != nil {
		return nil, err
	}
	ids := cfg.OrderedIDs()
	list := make([]AgentStatus, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			list[i] = CheckAgent(id, cfg.Agents[id])
		}(i, id)
	}
	wg.Wait()
	statusMu.Lock()
	statusCache, statusAt = list, time.Now()
	statusMu.Unlock()
	return list, nil
}

func readyIDs() ([]string, error) {
	list, err := AgentStatuses(false)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for _, s := range list {
		if s.Status == "ready" {
			ids = append(ids, s.ID)
		}
	}
	return ids, nil
}

func GetRun(id string) *Run {
	runsMu.Lock()
	defer runsMu.Unlock()
	return runs[id]
}

func ListRuns() []RunSummary {
	runsMu.Lock()
	defer runsMu.Unlock()
	out := []RunSummary{}
	for i := len(runOrder) - 1; i >= 0; i-- {
		out = append(out, runs[runOrder[i]].Summary())
	}
	return out
}

func CancelAllRuns() {
	runsMu.Lock()
	defer runsMu.Unlock()
	for _, r := range runs {
		r.Cancel()
	}
}

func launch(r *Run, work func() (*RunResult, error)) *Run {
	runsMu.Lock()
	runs[r.ID] = r
	runOrder = append(runOrder, r.ID)
	runsMu.Unlock()
	go func() {
		defer func() {
			if p := recover(); p != nil {
				r.fail(errors.New("internal error: " + strings.TrimSpace(fmtAny(p))))
			}
		}()
		res, err := work()
		if err != nil {
			r.fail(err)
			return
		}
		r.finish(res)
	}()
	return r
}

func fmtAny(v any) string {
	if e, ok := v.(error); ok {
		return e.Error()
	}
	if s, ok := v.(string); ok {
		return s
	}
	return "unexpected failure"
}

func firstLine(s string, max int) string {
	l := strings.TrimSpace(strings.SplitN(strings.TrimSpace(s), "\n", 2)[0])
	if len([]rune(l)) > max {
		l = string([]rune(l)[:max])
	}
	return l
}

type CouncilRequest struct {
	Question    string       `json:"question"`
	Agents      []string     `json:"agents"`
	Rounds      flexInt      `json:"rounds"`
	Chair       string       `json:"chair"`
	Attachments []Attachment `json:"attachments"`
}

func StartCouncil(req CouncilRequest) (*Run, error) {
	q := strings.TrimSpace(req.Question)
	if q == "" {
		return nil, errors.New("Ask the council something first.")
	}
	cfg, err := LoadConfig()
	if err != nil {
		return nil, err
	}
	EnsureHome()
	ready, err := readyIDs()
	if err != nil {
		return nil, err
	}
	want := req.Agents
	if len(want) == 0 {
		want = cfg.OrderedIDs()
	}
	members := []string{}
	for _, id := range want {
		if contains(ready, id) {
			members = append(members, id)
		}
	}
	if len(members) == 0 {
		return nil, errors.New("None of the chosen AIs are ready. Open Setup to see what needs installing, signing in, or a key.")
	}
	rounds := int(req.Rounds)
	if rounds <= 0 {
		rounds = cfg.CouncilRounds
	}
	id := NewRunID()
	runDir := filepath.Join(HomeDir(), "council", id)
	r, err := NewRun("council", firstLine(q, 80), cfg, ready, runDir, runDir)
	if err != nil {
		return nil, err
	}
	return launch(r, func() (*RunResult, error) {
		return RunCouncil(r, CouncilOpts{Question: q, Agents: members, Rounds: rounds, Chair: req.Chair, Attachments: req.Attachments})
	}), nil
}

type BuildRequest struct {
	Dir             string            `json:"dir"`
	Task            string            `json:"task"`
	Roles           map[string]string `json:"roles"`
	MaxReviewRounds *flexInt          `json:"maxReviewRounds"`
	TestCommand     string            `json:"testCommand"`
	AllowDirty      bool              `json:"allowDirty"`
	NoBranch        bool              `json:"noBranch"`
	NoResearch      bool              `json:"noResearch"`
	Research        *bool             `json:"research"`
}

func StartBuild(req BuildRequest) (*Run, error) {
	task := strings.TrimSpace(req.Task)
	if task == "" {
		return nil, errors.New("Describe what to build first.")
	}
	dir := strings.Trim(strings.TrimSpace(req.Dir), `"`)
	if dir == "" {
		return nil, errors.New("Choose a project folder.")
	}
	if dir == "~" || strings.HasPrefix(dir, "~/") || strings.HasPrefix(dir, `~\`) {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, dir[1:])
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, err
	}
	if st, err := os.Stat(abs); err != nil || !st.IsDir() {
		return nil, errors.New(abs + " is not a folder.")
	}
	cfg, err := LoadConfig()
	if err != nil {
		return nil, err
	}
	ready, err := readyIDs()
	if err != nil {
		return nil, err
	}
	if len(ready) == 0 {
		return nil, errors.New("No AIs are ready yet. Open Setup to see what needs installing or signing in.")
	}
	id := NewRunID()
	gi, err := PrepareGit(abs, slugOr(task), id, req.AllowDirty, !req.NoBranch)
	if err != nil {
		return nil, err
	}
	r, err := NewRun("build", firstLine(task, 80), cfg, ready, filepath.Join(abs, ".ensemble", "runs", id), abs)
	if err != nil {
		return nil, err
	}
	rounds := cfg.MaxReviewRounds
	if req.MaxReviewRounds != nil {
		rounds = int(*req.MaxReviewRounds)
	}
	research := !req.NoResearch && (req.Research == nil || *req.Research)
	return launch(r, func() (*RunResult, error) {
		return RunBuild(r, BuildOpts{Dir: abs, Task: task, Roles: req.Roles, MaxReviewRounds: rounds, TestCommand: strings.TrimSpace(req.TestCommand), Research: research, Git: gi})
	}), nil
}

func slugOr(s string) string {
	if v := slug(s, 30); v != "" {
		return v
	}
	return "task"
}

// flexInt accepts 2 or "2" from JSON (HTML selects send strings).
type flexInt int

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return err
	}
	*f = flexInt(n)
	return nil
}

var (
	pingMu   sync.Mutex
	lastPing = time.Now()
)

// Ping records that the dashboard is still open (used to exit after the
// browser tab closes when Ensemble runs without its own window).
func Ping() {
	pingMu.Lock()
	lastPing = time.Now()
	pingMu.Unlock()
}

func LastPing() time.Time {
	pingMu.Lock()
	defer pingMu.Unlock()
	return lastPing
}

func AnyRunning() bool {
	runsMu.Lock()
	defer runsMu.Unlock()
	for _, r := range runs {
		if r.Status() == "running" {
			return true
		}
	}
	return false
}
