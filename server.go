package main

// Dashboard server. The desktop server binds to 127.0.0.1 with a per-launch
// token. The phone server (phone.go) uses a fixed port and a long-lived
// pairing token, and is reached only through your private Tailscale network.
// Both reject requests addressed to unexpected host names, so other websites
// in a browser can't start runs on your computer.

import (
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

//go:embed ui/index.html
var uiHTML []byte

//go:embed assets/icon-192.png
var icon192 []byte

//go:embed assets/icon.png
var icon512 []byte

const manifestJSON = `{
  "name": "Ensemble",
  "short_name": "Ensemble",
  "description": "Your AIs working as one team",
  "start_url": "/",
  "scope": "/",
  "display": "standalone",
  "background_color": "#f6f5f1",
  "theme_color": "#1c1b19",
  "icons": [
    {"src": "/icon-192.png", "sizes": "192x192", "type": "image/png", "purpose": "any"},
    {"src": "/icon-512.png", "sizes": "512x512", "type": "image/png", "purpose": "any"}
  ]
}`

// A minimal service worker: it makes the page installable as an app. It never
// caches anything, because Ensemble only works while the PC is reachable.
const serviceWorkerJS = `self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', (e) => e.waitUntil(self.clients.claim()));
self.addEventListener('fetch', () => {});
`

type Server struct {
	Token     string
	Port      int
	URL       string
	Remote    bool // true for the phone server
	listeners []net.Listener
	http      *http.Server
	hostsMu   sync.RWMutex
	hosts     map[string]bool
}

// AllowHost lets requests addressed to another name reach this server
// (used for the Tailscale HTTPS name).
func (s *Server) AllowHost(h string) {
	s.hostsMu.Lock()
	s.hosts[h] = true
	s.hostsMu.Unlock()
}

func (s *Server) hostOK(h string) bool {
	s.hostsMu.RLock()
	defer s.hostsMu.RUnlock()
	return s.hosts[h]
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

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// NewServer is the desktop server: 127.0.0.1:port (0 picks a free port) with a
// fresh token each launch.
func NewServer(port int) (*Server, error) {
	return newServerOn(randomToken(16), port, nil, false)
}

// newServerOn always listens on 127.0.0.1 and also on any extra IPs given.
func newServerOn(token string, port int, extraIPs []string, remote bool) (*Server, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, err
	}
	p := ln.Addr().(*net.TCPAddr).Port
	s := &Server{
		Token:     token,
		Port:      p,
		Remote:    remote,
		listeners: []net.Listener{ln},
		hosts:     map[string]bool{fmt.Sprintf("127.0.0.1:%d", p): true, fmt.Sprintf("localhost:%d", p): true},
	}
	for _, ip := range extraIPs {
		addr := net.JoinHostPort(ip, fmt.Sprint(p))
		l, err := net.Listen("tcp", addr)
		if err != nil {
			continue // that address isn't usable right now; the others still work
		}
		s.listeners = append(s.listeners, l)
		s.hosts[addr] = true
	}
	s.URL = fmt.Sprintf("http://127.0.0.1:%d/?token=%s", p, s.Token)
	s.http = &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second}
	return s, nil
}

// newServerWithListeners serves on listeners someone else opened (the phone
// server on the built-in Tailscale device). Host names are added with AllowHost.
func newServerWithListeners(token string, lns []net.Listener, remote bool) *Server {
	s := &Server{Token: token, Remote: remote, listeners: lns, hosts: map[string]bool{}}
	s.http = &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second}
	return s
}

func (s *Server) Serve() error {
	errs := make(chan error, len(s.listeners))
	for _, l := range s.listeners {
		go func(l net.Listener) { errs <- s.http.Serve(l) }(l)
	}
	err := <-errs
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
	if !s.hostOK(r.Host) {
		sendErr(w, http.StatusForbidden, "Bad host")
		return
	}
	if r.Method == http.MethodGet {
		static := map[string]struct {
			ctype string
			body  []byte
		}{
			"/":                     {"text/html; charset=utf-8", uiHTML},
			"/manifest.webmanifest": {"application/manifest+json", []byte(manifestJSON)},
			"/sw.js":                {"text/javascript", []byte(serviceWorkerJS)},
			"/icon-192.png":         {"image/png", icon192},
			"/icon-512.png":         {"image/png", icon512},
		}
		if f, ok := static[r.URL.Path]; ok {
			w.Header().Set("Content-Type", f.ctype)
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			_, _ = w.Write(f.body)
			return
		}
	}
	tok := r.Header.Get("X-Ensemble-Token")
	if tok == "" {
		tok = r.URL.Query().Get("token")
	}
	if subtleEqual(tok, s.Token) == false {
		msg := "Missing or wrong token. Restart Ensemble."
		if s.Remote {
			msg = "This phone isn't paired, or pairing was reset. Scan the QR code in Ensemble on your PC (Phone button) again."
		}
		sendErr(w, http.StatusUnauthorized, msg)
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
			"canPick":    CanPickFolder() && !s.Remote,
			"remote":     s.Remote,
			"git":        gitInstalled(),
			"canInstall": InstallSupported() && !s.Remote,
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

	case r.URL.Path == "/api/phone" && !s.Remote:
		if r.Method == http.MethodPost {
			var body struct {
				Enable  *bool `json:"enable"`
				Reset   bool  `json:"reset"`
				SignOut bool  `json:"signOut"`
			}
			if err := readJSON(r, &body); err != nil {
				sendErr(w, 400, err.Error())
				return
			}
			switch {
			case body.SignOut:
				Phone.SignOut()
			case body.Reset:
				Phone.ResetPairing()
			case body.Enable != nil && *body.Enable:
				Phone.Enable()
			case body.Enable != nil:
				Phone.Disable()
			}
		} else if r.URL.Query().Has("refresh") {
			Phone.Refresh()
		}
		sendJSON(w, 200, Phone.Status())

	case r.Method == http.MethodPost && r.URL.Path == "/api/install" && !s.Remote:
		var body struct {
			ID string `json:"id"`
		}
		if err := readJSON(r, &body); err != nil {
			sendErr(w, 400, err.Error())
			return
		}
		out, err := Install(body.ID)
		if err != nil {
			sendJSON(w, 400, map[string]string{"error": err.Error(), "output": out})
			return
		}
		sendJSON(w, 200, map[string]any{"ok": true, "output": out})

	case r.Method == http.MethodPost && r.URL.Path == "/api/signin" && !s.Remote:
		var body struct {
			ID string `json:"id"`
		}
		if err := readJSON(r, &body); err != nil {
			sendErr(w, 400, err.Error())
			return
		}
		if err := SignIn(body.ID); err != nil {
			sendErr(w, 400, err.Error())
			return
		}
		sendJSON(w, 200, map[string]bool{"ok": true})

	case r.Method == http.MethodPost && r.URL.Path == "/api/pick-folder" && !s.Remote:
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
	// ?from=N resumes a stream after a phone wakes up, without repeating events.
	next := 0
	fmt.Sscanf(r.URL.Query().Get("from"), "%d", &next)
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

func subtleEqual(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func gitInstalled() bool {
	_, err := exec.LookPath("git")
	return err == nil
}
