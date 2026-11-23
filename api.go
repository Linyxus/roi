package main

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
)

//go:embed web
var webFS embed.FS

type Config struct {
	// Launch is the command that starts each kind of agent in a new pane.
	Launch map[string][]string `json:"launch"`
	// Interactive also reads the shell's interactive startup files, which
	// is needed when a launch command is a shell alias or function.
	Interactive bool   `json:"interactive"`
	PushContact string `json:"pushContact"`
	// Hostname is the tailnet device name.
	Hostname string `json:"hostname"`
}

// loadConfig reads config.json. A missing file is not an error; a file that
// does not parse is reported and the defaults are used.
func loadConfig(dir string) (Config, error) {
	c := Config{Launch: map[string][]string{"claude": {"claude"}, "codex": {"codex"}}, PushContact: "mailto:roi@example.com", Hostname: "roi"}
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return c, nil
	}
	var f Config
	if err := json.Unmarshal(b, &f); err != nil {
		return c, fmt.Errorf("config.json: %v", err)
	}
	for k, v := range f.Launch {
		if len(v) > 0 {
			c.Launch[k] = v
		}
	}
	c.Interactive = f.Interactive
	if f.PushContact != "" {
		c.PushContact = f.PushContact
	}
	if f.Hostname != "" {
		c.Hostname = f.Hostname
	}
	return c, nil
}

// deviceHeader carries the caller's Tailscale device name to the handlers.
// The listeners set it themselves, so a client cannot supply it.
const deviceHeader = "X-Roi-Device"

type Server struct {
	hub  *Hub
	rex  *Rex
	cfg  Config
	host string
	home string

	modes  sync.Map // agent id -> permission mode last read off its screen
	cycles sync.Map // agent id -> the modes it offers, once a switch has gone all the way round
	modeMu sync.Mutex
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func readBody(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
	return json.NewDecoder(r.Body).Decode(v)
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(webFS, "web")
	files := http.FileServer(http.FS(sub))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		if strings.HasSuffix(r.URL.Path, ".webmanifest") {
			w.Header().Set("Content-Type", "application/manifest+json")
		}
		files.ServeHTTP(w, r)
	})

	mux.HandleFunc("GET /api/agents", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"agents": s.hub.List(), "host": s.host})
	})

	mux.HandleFunc("GET /api/events", func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			fail(w, 500, errors.New("streaming unsupported"))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store")
		ch, cancel := s.hub.Subscribe()
		defer cancel()
		fmt.Fprint(w, "data: hello\n\n")
		fl.Flush()
		ping := time.NewTicker(25 * time.Second)
		defer ping.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ch:
				fmt.Fprint(w, "data: agents\n\n")
			case <-ping.C:
				fmt.Fprint(w, ": ping\n\n")
			}
			fl.Flush()
		}
	})

	agent := func(fn func(w http.ResponseWriter, r *http.Request, a Agent)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			a, ok := s.hub.Get(r.PathValue("id"))
			if !ok {
				fail(w, 404, errors.New("that agent is no longer running"))
				return
			}
			fn(w, r, a)
		}
	}

	mux.HandleFunc("GET /api/agents/{id}", agent(func(w http.ResponseWriter, r *http.Request, a Agent) {
		out := map[string]any{}
		rev := 0
		if a.tr != nil {
			a.tr.Update()
			items, plan, mode, truncated, trev := a.tr.Snapshot(400)
			out["items"], out["plan"], out["mode"], out["truncated"] = items, plan, mode, truncated
			rev = trev
		} else {
			out["items"], out["noTranscript"] = []Item{}, true
		}
		if a.State == "need" {
			out["prompt"] = s.hub.BuildPrompt(a)
		}
		if a.Kind == "claude" {
			// The screen has the mode as it is now; a prompt can cover the
			// footer, so the last one read is kept.
			if screen, err := s.rex.screenText(a.Session, a.block, 6); err == nil {
				if m := screenMode(screen); m != "" {
					s.modes.Store(a.ID, m)
				}
			}
			if m, ok := s.modes.Load(a.ID); ok {
				out["mode"] = m
			}
			if c, ok := s.cycles.Load(a.ID); ok {
				out["modes"] = c
			}
		}
		if a.State == "done" && !a.Unread {
			a.State = "idle"
		}
		out["agent"] = a
		// One stamp covers everything the session view shows.
		pb, _ := json.Marshal(out["prompt"])
		ab, _ := json.Marshal(a)
		stamp := fmt.Sprintf("%s|%d|%s|%s|%v", a.tr.pathOrEmpty(), rev, ab, pb, out["mode"])
		out["stamp"] = fnv(stamp)
		if r.URL.Query().Get("stamp") == out["stamp"] {
			writeJSON(w, map[string]any{"unchanged": true})
			return
		}
		writeJSON(w, out)
	}))

	mux.HandleFunc("POST /api/agents/{id}/seen", agent(func(w http.ResponseWriter, r *http.Request, a Agent) {
		s.hub.MarkSeen(a.ID)
		writeJSON(w, map[string]bool{"ok": true})
	}))

	mux.HandleFunc("POST /api/agents/{id}/send", agent(func(w http.ResponseWriter, r *http.Request, a Agent) {
		var b struct {
			Text string `json:"text"`
		}
		if err := readBody(r, &b); err != nil || strings.TrimSpace(b.Text) == "" {
			fail(w, 400, errors.New("nothing to send"))
			return
		}
		if err := s.submit(a, b.Text); err != nil {
			fail(w, 502, err)
			return
		}
		s.hub.MarkSeen(a.ID)
		s.hub.Nudge(a)
		writeJSON(w, map[string]bool{"ok": true})
	}))

	// Claude Code only has a key that steps to the next mode, so choosing a
	// mode means pressing it until the screen shows the one asked for.
	mux.HandleFunc("POST /api/agents/{id}/mode", agent(func(w http.ResponseWriter, r *http.Request, a Agent) {
		var b struct {
			Mode string `json:"mode"`
		}
		known := []string{"default", "acceptEdits", "plan", "auto", "bypassPermissions"}
		if err := readBody(r, &b); err != nil || !slices.Contains(known, b.Mode) || a.Kind != "claude" {
			fail(w, 400, errors.New("no such mode"))
			return
		}
		s.modeMu.Lock()
		defer s.modeMu.Unlock()
		read := func() string {
			screen, err := s.rex.screenText(a.Session, a.block, 6)
			if err != nil {
				return ""
			}
			return screenMode(screen)
		}
		cur := read()
		if cur == "" {
			fail(w, 409, errors.New("the agent's screen is not showing its mode right now; try again in a moment"))
			return
		}
		seen := []string{cur}
		for cur != b.Mode {
			if err := s.rex.keys(a.Session, a.block, "shift+tab"); err != nil {
				fail(w, 502, err)
				return
			}
			next := ""
			for i := 0; i < 20 && next == ""; i++ {
				time.Sleep(60 * time.Millisecond)
				if m := read(); m != cur {
					next = m
				}
			}
			if next == "" {
				s.modes.Store(a.ID, cur)
				fail(w, 502, errors.New("the agent did not switch modes"))
				return
			}
			cur = next
			s.modes.Store(a.ID, cur)
			if slices.Contains(seen, cur) { // all the way round without finding it
				s.cycles.Store(a.ID, seen)
				fail(w, 409, errors.New("this agent does not offer that mode"))
				return
			}
			seen = append(seen, cur)
		}
		s.modes.Store(a.ID, cur)
		writeJSON(w, map[string]string{"mode": cur})
	}))

	mux.HandleFunc("POST /api/agents/{id}/key", agent(func(w http.ResponseWriter, r *http.Request, a Agent) {
		var b struct {
			Keys []string `json:"keys"`
			Text string   `json:"text"`
		}
		if err := readBody(r, &b); err != nil || (len(b.Keys) == 0 && b.Text == "") || len(b.Keys) > 32 {
			fail(w, 400, errors.New("no keys given"))
			return
		}
		var err error
		if b.Text != "" {
			err = s.rex.paste(a.Session, a.block, b.Text)
		}
		if err == nil && len(b.Keys) > 0 {
			err = s.rex.keys(a.Session, a.block, b.Keys...)
		}
		if err != nil {
			fail(w, 502, err)
			return
		}
		s.hub.Nudge(a)
		writeJSON(w, map[string]bool{"ok": true})
	}))

	// Answer a prompt: pick a numbered option, or dismiss it and say
	// something else instead.
	mux.HandleFunc("POST /api/agents/{id}/answer", agent(func(w http.ResponseWriter, r *http.Request, a Agent) {
		var b struct {
			Option int    `json:"option"`
			Text   string `json:"text"`
		}
		if err := readBody(r, &b); err != nil {
			fail(w, 400, err)
			return
		}
		if a.State != "need" {
			fail(w, 409, errors.New("this agent is no longer waiting"))
			return
		}
		var err error
		switch {
		case b.Option >= 1 && b.Option <= 9:
			err = s.rex.keys(a.Session, a.block, itoa(b.Option))
		case strings.TrimSpace(b.Text) != "":
			if err = s.rex.keys(a.Session, a.block, "escape"); err == nil {
				time.Sleep(600 * time.Millisecond)
				err = s.submit(a, b.Text)
			}
		default:
			err = errors.New("no answer given")
		}
		if err != nil {
			fail(w, 502, err)
			return
		}
		s.hub.Nudge(a)
		writeJSON(w, map[string]bool{"ok": true})
	}))

	mux.HandleFunc("GET /api/agents/{id}/screen", agent(func(w http.ResponseWriter, r *http.Request, a Agent) {
		html, err := s.rex.screenHTML(a.Session, a.block)
		if err != nil {
			fail(w, 502, err)
			return
		}
		sz := s.rex.size(a.Session, a.block)
		writeJSON(w, map[string]any{"html": sanitizeTermHTML(html), "cols": sz.Columns, "rows": sz.Rows})
	}))

	mux.HandleFunc("GET /api/projects", func(w http.ResponseWriter, r *http.Request) {
		type proj struct {
			Name    string `json:"name"`
			Cwd     string `json:"cwd"`
			Running int    `json:"running"`
		}
		by := map[string]*proj{}
		var order []string
		for _, a := range s.hub.List() {
			if a.Cwd == "" {
				continue
			}
			p := by[a.Cwd]
			if p == nil {
				p = &proj{Name: a.Project, Cwd: a.Cwd}
				by[a.Cwd] = p
				order = append(order, a.Cwd)
			}
			p.Running++
		}
		out := []proj{}
		for _, c := range order {
			out = append(out, *by[c])
		}
		kinds := []string{}
		for k := range s.cfg.Launch {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)
		writeJSON(w, map[string]any{"projects": out, "home": s.home, "kinds": kinds})
	})

	mux.HandleFunc("GET /api/dirs", func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("path")
		if p == "" {
			p = s.home
		}
		p = filepath.Clean(p)
		ents, err := os.ReadDir(p)
		if err != nil {
			fail(w, 400, errors.New("cannot open that folder"))
			return
		}
		dirs := []string{}
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			if e.IsDir() {
				dirs = append(dirs, e.Name())
			} else if e.Type()&fs.ModeSymlink != 0 {
				if st, err := os.Stat(filepath.Join(p, e.Name())); err == nil && st.IsDir() {
					dirs = append(dirs, e.Name())
				}
			}
		}
		sort.Slice(dirs, func(i, j int) bool { return strings.ToLower(dirs[i]) < strings.ToLower(dirs[j]) })
		writeJSON(w, map[string]any{"path": p, "parent": filepath.Dir(p), "dirs": dirs})
	})

	mux.HandleFunc("POST /api/spawn", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Kind, Cwd, Prompt string
		}
		if err := readBody(r, &b); err != nil {
			fail(w, 400, err)
			return
		}
		id, err := s.spawn(b.Kind, b.Cwd, b.Prompt)
		if err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, map[string]string{"id": id})
	})

	mux.HandleFunc("GET /api/push", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"key": s.hub.push.Public, "devices": s.hub.push.Count()})
	})
	mux.HandleFunc("POST /api/push/subscribe", func(w http.ResponseWriter, r *http.Request) {
		var sub webpush.Subscription
		if err := readBody(r, &sub); err != nil || !strings.HasPrefix(sub.Endpoint, "https://") || sub.Keys.Auth == "" {
			fail(w, 400, errors.New("not a push subscription"))
			return
		}
		s.hub.push.Add(sub, DeviceMeta{Name: r.Header.Get(deviceHeader), UA: r.UserAgent(), Added: time.Now()})
		writeJSON(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/push/test", func(w http.ResponseWriter, r *http.Request) {
		n := s.hub.push.Notify("", "roi", "Notifications are working.", "done")
		writeJSON(w, map[string]int{"delivered": n})
	})

	return guard(mux)
}

// guard rejects cross-site writes: every state-changing request must carry
// a header that a plain cross-origin form or fetch cannot set.
func guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("X-Roi") != "1" {
			fail(w, 403, errors.New("missing X-Roi header"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// submit pastes text into the agent's input and presses Return.
func (s *Server) submit(a Agent, text string) error {
	if err := s.rex.paste(a.Session, a.block, strings.TrimRight(text, "\n")); err != nil {
		return err
	}
	time.Sleep(250 * time.Millisecond)
	return s.rex.keys(a.Session, a.block, "enter")
}

// spawn starts a new agent pane and returns its agent id.
func (s *Server) spawn(kind, cwd, prompt string) (string, error) {
	launch, ok := s.cfg.Launch[kind]
	if !ok {
		return "", fmt.Errorf("unknown agent kind %q", kind)
	}
	st, err := os.Stat(cwd)
	if err != nil || !st.IsDir() || !filepath.IsAbs(cwd) {
		return "", errors.New("that folder does not exist on the Mac")
	}
	if real, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = real // agents report resolved paths
	}
	cmd := append([]string{}, launch...)
	if p := strings.TrimSpace(prompt); p != "" {
		cmd = append(cmd, p)
	}
	name := filepath.Base(cwd)
	// Reuse the Rex session that already works in this folder, if any.
	session := ""
	for _, a := range s.hub.List() {
		if a.Cwd == cwd {
			session = a.Session
			break
		}
	}
	var args []string
	if session != "" {
		args = []string{"window", "new", "-s", session, name, "-c", cwd, "--focus=false", "--json"}
	} else {
		args = []string{"new", name, "-c", cwd, "--json"}
	}
	if s.cfg.Interactive {
		args = append(args, "--interactive")
	}
	args = append(append(args, "--"), cmd...)
	out, err := s.rex.run(args...)
	if err != nil {
		return "", err
	}
	var v struct {
		SessionID string   `json:"session_id"`
		BlockIDs  []string `json:"block_ids"`
		Initial   []struct {
			BlockIDs []string `json:"block_ids"`
		} `json:"initial_windows"`
	}
	json.Unmarshal(out, &v)
	block := ""
	if len(v.BlockIDs) > 0 {
		block = v.BlockIDs[0]
	} else if len(v.Initial) > 0 && len(v.Initial[0].BlockIDs) > 0 {
		block = v.Initial[0].BlockIDs[0]
	}
	if block == "" {
		return "", errors.New("Rex did not report the new pane")
	}
	created := session == ""
	if created {
		session = v.SessionID
	}
	// A launch command that fails exits at once; report it instead of
	// leaving a dead pane behind.
	for i := 0; i < 6; i++ {
		time.Sleep(400 * time.Millisecond)
		if !s.rex.exited(session, block) {
			continue
		}
		why, _ := s.rex.screenText(session, block, 4)
		if created {
			s.rex.run("kill", session)
		} else {
			s.rex.run("block", "close", "-s", session, block)
		}
		return "", fmt.Errorf("the agent did not start: %s", firstNonEmpty(strings.Join(strings.Fields(why), " "), "its command exited"))
	}
	select {
	case s.hub.dirty <- dirtyReq{}:
	default:
	}
	return agentID(block), nil
}

func (t *Transcript) pathOrEmpty() string {
	if t == nil {
		return ""
	}
	return t.Path
}

func fnv(s string) string {
	h := uint64(14695981039346656037)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return fmt.Sprintf("%x", h)
}
