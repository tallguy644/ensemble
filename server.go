package main

// Local dashboard server. Binds to 127.0.0.1 only, rejects requests addressed
// to any other host name, and requires a per-launch token, so other websites
// in a browser can't start runs on your computer.

import (
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

//go:embed ui/index.html
var uiHTML []byte

type Server struct {
	Token    string
	Port     int
	URL      string
	listener net.Listener
	http     *http.Server
	hosts    map[string]bool
}

func recentFile() string { return filepath.Join(HomeDir(), "recent-folders.json") }

func readRecent() []string {
	list := []string{}
	if b, err := os.ReadFile(recentFile()); err == nil {
		_ = json.Unmarshal(b, &list)
	}
	return list
}

func addRecent(dir string) {
	EnsureHome()
	list := []string{dir}
	for _, d := range readRecent() {
		if d != dir && len(list) < 8 {
			list = append(list, d)
		}
	}
	b, _ := json.MarshalIndent(list, "", "  ")
	_ = os.WriteFile(recentFile(), b, 0o600)
}

// NewServer listens on 127.0.0.1:port (port 0 picks a free one).
func NewServer(port int) (*Server, error) {
	tok := make([]byte, 16)
	if _, err := rand.Read(tok); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, err
	}
	p := ln.Addr().(*net.TCPAddr).Port
	s := &Server{
		Token:    hex.EncodeToString(tok),
		Port:     p,
		listener: ln,
		hosts:    map[string]bool{fmt.Sprintf("127.0.0.1:%d", p): true, fmt.Sprintf("localhost:%d", p): true},
	}
	s.URL = fmt.Sprintf("http://127.0.0.1:%d/?token=%s", p, s.Token)
	s.http = &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second}
	return s, nil
}

func (s *Server) Serve() error {
	err := s.http.Serve(s.listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) Close() { _ = s.http.Close() }

func sendJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func sendErr(w http.ResponseWriter, code int, msg string) {
	sendJSON(w, code, map[string]string{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	b, err := io.ReadAll(io.LimitReader(r.Body, 5_000_001))
	if err != nil {
		return err
	}
	if len(b) > 5_000_000 {
		return errors.New("Request too large.")
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil
	}
	return json.Unmarshal(b, v)
}

var runPath = regexp.MustCompile(`^/api/runs/([\w-]+)/(events|cancel)$`)

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Block DNS rebinding: only accept requests addressed to this local server.
	if !s.hosts[r.Host] {
		sendErr(w, http.StatusForbidden, "Bad host")
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(uiHTML)
		return
	}
	tok := r.Header.Get("X-Ensemble-Token")
	if tok == "" {
		tok = r.URL.Query().Get("token")
	}
	if tok != s.Token {
		sendErr(w, http.StatusUnauthorized, "Missing or wrong token. Restart Ensemble.")
		return
	}

	Ping()
	switch {
	case r.URL.Path == "/api/ping":
		sendJSON(w, 200, map[string]bool{"ok": true})

	case r.Method == http.MethodGet && r.URL.Path == "/api/agents":
		cfg, err := LoadConfig()
		if err != nil {
			sendErr(w, 400, err.Error())
			return
		}
		list, err := AgentStatuses(r.URL.Query().Has("refresh"))
		if err != nil {
			sendErr(w, 400, err.Error())
			return
		}
		sendJSON(w, 200, map[string]any{
			"agents":     list,
			"roles":      cfg.Roles,
			"defaults":   map[string]int{"councilRounds": cfg.CouncilRounds, "maxReviewRounds": cfg.MaxReviewRounds},
			"recent":     readRecent(),
			"canPick":    CanPickFolder(),
			"configPath": ConfigPath(),
		})

	case r.Method == http.MethodPost && r.URL.Path == "/api/keys":
		var body struct {
			Name   string `json:"name"`
			Key    string `json:"key"`
			Remove bool   `json:"remove"`
		}
		if err := readJSON(r, &body); err != nil {
			sendErr(w, 400, err.Error())
			return
		}
		var err error
		if body.Remove {
			err = RemoveKey(body.Name)
		} else {
			err = SetKey(body.Name, body.Key)
		}
		if err != nil {
			sendErr(w, 400, err.Error())
			return
		}
		ClearStatusCache()
		sendJSON(w, 200, map[string]bool{"ok": true})

	case r.Method == http.MethodPost && r.URL.Path == "/api/pick-folder":
		dir, err := PickFolder()
		if err != nil {
			sendErr(w, 400, err.Error())
			return
		}
		sendJSON(w, 200, map[string]string{"dir": dir})

	case r.Method == http.MethodGet && r.URL.Path == "/api/runs":
		sendJSON(w, 200, ListRuns())

	case r.Method == http.MethodPost && r.URL.Path == "/api/council":
		var req CouncilRequest
		if err := readJSON(r, &req); err != nil {
			sendErr(w, 400, err.Error())
			return
		}
		run, err := StartCouncil(req)
		if err != nil {
			sendErr(w, 400, err.Error())
			return
		}
		sendJSON(w, 200, run.Summary())

	case r.Method == http.MethodPost && r.URL.Path == "/api/build":
		var req BuildRequest
		if err := readJSON(r, &req); err != nil {
			sendErr(w, 400, err.Error())
			return
		}
		run, err := StartBuild(req)
		if err != nil {
			sendErr(w, 400, err.Error())
			return
		}
		addRecent(run.Cwd)
		sendJSON(w, 200, run.Summary())

	default:
		m := runPath.FindStringSubmatch(r.URL.Path)
		if m == nil {
			sendErr(w, 404, "Not found")
			return
		}
		run := GetRun(m[1])
		if run == nil {
			sendErr(w, 404, "Unknown run")
			return
		}
		if m[2] == "cancel" && r.Method == http.MethodPost {
			run.Cancel()
			sendJSON(w, 200, map[string]bool{"ok": true})
			return
		}
		if m[2] == "events" && r.Method == http.MethodGet {
			s.streamEvents(w, r, run)
			return
		}
		sendErr(w, 404, "Not found")
	}
}

func (s *Server) streamEvents(w http.ResponseWriter, r *http.Request, run *Run) {
	fl, ok := w.(http.Flusher)
	if !ok {
		sendErr(w, 500, "Streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(200)
	write := func(e Event) {
		b, _ := json.Marshal(e)
		fmt.Fprintf(w, "data: %s\n\n", b)
	}
	sum := run.Summary()
	write(Event{Type: "hello", Run: &sum})
	fl.Flush()

	wake := run.Subscribe()
	defer run.Unsubscribe(wake)
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	next := 0
	for {
		for _, e := range run.EventsFrom(next) {
			next++
			write(e)
			if e.Type == "done" || e.Type == "error" {
				fl.Flush()
				return
			}
		}
		fl.Flush()
		select {
		case <-wake:
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		case <-r.Context().Done():
			return
		}
	}
}
