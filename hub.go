package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type Worker struct {
	Title    string `json:"title"`
	State    string `json:"state"`
	Progress *int   `json:"progress,omitempty"`
}

// Agent is one Claude Code or Codex session running in a Rex terminal pane.
type Agent struct {
	ID           string   `json:"id"`
	Kind         string   `json:"kind"` // claude | codex
	Title        string   `json:"title"`
	Project      string   `json:"project"`
	Cwd          string   `json:"cwd"`
	Session      string   `json:"session"`
	SessionLabel string   `json:"sessionLabel"`
	State        string   `json:"state"` // need | done | working | idle
	Need         string   `json:"need,omitempty"`
	Line         string   `json:"line"`
	Updated      string   `json:"updated"`
	Unread       bool     `json:"unread"`
	Workers      []Worker `json:"workers,omitempty"`

	block string
	pid   int
	tr    *Transcript
}

type Hub struct {
	rex  *Rex
	home string
	dir  string // state directory

	mu     sync.Mutex
	agents map[string]*Agent
	seen   map[string]string // agent id -> updated stamp the user has seen
	last   string            // last broadcast JSON
	subs   map[chan struct{}]struct{}
	ready  bool

	dirty chan dirtyReq
	push  *Pusher
}

type dirtyReq struct{ session, block string }

func newHub(rex *Rex, home, dir string) *Hub {
	h := &Hub{rex: rex, home: home, dir: dir, agents: map[string]*Agent{}, seen: map[string]string{},
		subs: map[chan struct{}]struct{}{}, dirty: make(chan dirtyReq, 256)}
	if b, err := os.ReadFile(filepath.Join(dir, "seen.json")); err == nil {
		json.Unmarshal(b, &h.seen)
	}
	return h
}

func agentID(block string) string { return strings.TrimPrefix(block, "block:") }

// Run keeps the agent list current until ctx ends.
func (h *Hub) Run(ctx context.Context) {
	h.refreshAll()
	h.mu.Lock()
	h.ready = true
	h.mu.Unlock()
	go func() {
		for ctx.Err() == nil {
			err := h.rex.events(ctx, func(session, block, name string) {
				select {
				case h.dirty <- dirtyReq{session, block}:
				default:
				}
			})
			if ctx.Err() == nil {
				log.Printf("rex events ended (%v); reconnecting", err)
				time.Sleep(2 * time.Second)
			}
		}
	}()
	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			h.refreshAll()
		case d := <-h.dirty:
			// Coalesce a burst of events into one pass.
			full := d.block == ""
			blocks := map[string]string{d.block: d.session}
			timer := time.After(250 * time.Millisecond)
		drain:
			for {
				select {
				case d2 := <-h.dirty:
					if d2.block == "" {
						full = true
					}
					blocks[d2.block] = d2.session
				case <-timer:
					break drain
				}
			}
			if full {
				h.refreshAll()
				continue
			}
			known := true
			h.mu.Lock()
			for b := range blocks {
				if _, ok := h.agents[agentID(b)]; !ok {
					known = false
				}
			}
			h.mu.Unlock()
			if !known { // a pane we do not track yet may have become an agent
				h.refreshAll()
				continue
			}
			for b, s := range blocks {
				h.refreshBlock(s, "", b)
			}
			h.broadcast()
		}
	}
}

func (h *Hub) refreshAll() {
	sessions, err := h.rex.sessions()
	if err != nil {
		log.Printf("rex: %v", err)
		return
	}
	live := map[string]bool{}
	var lmu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, s := range sessions {
		blocks, err := h.rex.blocks(s.ID)
		if err != nil {
			continue
		}
		for _, b := range blocks {
			if b.Creator != "com.superlogical.terminal" {
				continue
			}
			wg.Add(1)
			sem <- struct{}{}
			go func(s rexSession, b rexBlock) {
				defer func() { <-sem; wg.Done() }()
				if h.refreshBlock(s.ID, s.Label, b.ID) {
					lmu.Lock()
					live[agentID(b.ID)] = true
					lmu.Unlock()
				}
			}(s, b)
		}
	}
	wg.Wait()
	h.mu.Lock()
	for id := range h.agents {
		if !live[id] {
			delete(h.agents, id)
		}
	}
	h.mu.Unlock()
	h.broadcast()
}

var shellTitle = regexp.MustCompile(`^\S+ [~/]\S*$`)

var titleJunk = regexp.MustCompile(`^[^\p{L}\p{N}]+`)

// refreshBlock re-reads one pane. It reports whether the pane holds an agent.
func (h *Hub) refreshBlock(session, sessionLabel, block string) bool {
	id := agentID(block)
	proc, perr := h.rex.process(session, block)
	recs, _ := h.rex.status(session, block)
	var root *statusRecord
	for i := range recs {
		if recs[i].ID == "" {
			root = &recs[i]
		}
	}
	kind := ""
	app := ""
	if root != nil {
		app = root.App
	}
	for _, r := range recs {
		if app == "" {
			app = r.App
		}
	}
	switch {
	case strings.Contains(app, "claude"):
		kind = "claude"
	case strings.Contains(app, "codex"):
		kind = "codex"
	case proc.Argv0 == "claude" || strings.Contains(proc.Path, "/claude/versions/"):
		kind = "claude"
	case strings.Contains(proc.Argv0, "codex") || strings.Contains(proc.Name, "codex"):
		kind = "codex"
	}
	// A shell that once ran an agent may keep its last status record. The
	// pane counts as an agent when the foreground program is the agent, or
	// is the same process that reported the status (an agent started by a
	// wrapper such as `fish -c`).
	live := !isShell(proc.Name)
	if !live && root != nil && root.Owner != nil && root.Owner.PID == proc.PID && root.State != "idle" {
		live = true
	}
	if !live && root != nil && root.Owner != nil && root.Owner.PID == proc.PID {
		live = len(descendants(proc.PID)) > 0
	}
	if perr != nil || kind == "" || !live {
		h.mu.Lock()
		delete(h.agents, id)
		h.mu.Unlock()
		return false
	}

	h.mu.Lock()
	a := h.agents[id]
	if a == nil {
		a = &Agent{ID: id, block: block}
	}
	prev := *a
	h.mu.Unlock()

	n := prev
	n.Kind, n.Session, n.Cwd, n.pid = kind, session, proc.Cwd, proc.PID
	if sessionLabel != "" {
		n.SessionLabel = sessionLabel
	}
	n.Project = filepath.Base(proc.Cwd)
	title := strings.TrimSpace(titleJunk.ReplaceAllString(h.rex.title(session, block), ""))
	// Agents that set no title leave the shell's own, "command ~/path".
	if title == "" || title == "Claude Code" || title == "codex" || shellTitle.MatchString(title) {
		title = n.Project
	}
	n.Title = title

	n.State, n.Need, n.Workers = "idle", "", nil
	msg := ""
	if root != nil {
		n.Updated = root.UpdatedAt
		msg = root.Msg
		switch root.State {
		case "working":
			n.State = "working"
		case "blocked":
			n.State, n.Need = "need", root.Kind
		case "done", "error":
			n.State = "done"
		}
	}
	for _, r := range recs {
		if r.ID == "" {
			continue
		}
		if r.State == "blocked" && n.State != "need" {
			n.State, n.Need = "need", r.Kind
			n.Updated = r.UpdatedAt
		}
		if r.State == "working" || r.State == "blocked" || r.State == "done" {
			t := r.Title
			if t == "" {
				t = r.Msg
			}
			if t != "" {
				n.Workers = append(n.Workers, Worker{t, r.State, r.Progress})
			}
		}
	}
	if n.Updated == "" {
		n.Updated = prev.Updated
	}

	// Find the transcript; a /clear or resume changes the file.
	path := ""
	if kind == "claude" {
		path = claudeTranscriptPath(h.home, proc.PID)
		if path == "" { // started through a wrapper: the agent is a child
			for _, c := range descendants(proc.PID) {
				if path = claudeTranscriptPath(h.home, c); path != "" {
					break
				}
			}
		}
	} else {
		path = codexTranscriptPath(h.home, proc.Cwd)
	}
	if path != "" && (n.tr == nil || n.tr.Path != path) {
		n.tr = newTranscript(kind, path)
	}

	switch n.State {
	case "working":
		active := ""
		for _, w := range n.Workers {
			if w.State == "working" {
				active = w.Title
			}
		}
		n.Line = firstNonEmpty(msg, active, "Working")
	case "need":
		n.Line = firstNonEmpty(msg, "Waiting for you")
		if n.tr != nil {
			n.tr.Update()
			if p := n.tr.PendingTool(); p != nil {
				if len(p.Questions) > 0 {
					n.Line = "Asks: " + p.Questions[0].Question
					if n.Need == "" {
						n.Need = "question"
					}
				} else if p.Name == "Bash" {
					n.Line = "Wants to run " + p.Summary
				} else {
					n.Line = strings.TrimSpace("Wants to use " + p.Name + " " + p.Summary)
				}
			}
		}
	case "done":
		n.Line = firstNonEmpty(msg, "Finished")
		if n.tr != nil {
			n.tr.Update()
			if s := oneLine(n.tr.LastAssistant()); s != "" {
				n.Line = s
			}
		}
	default:
		n.Line = "Idle"
		if n.tr != nil {
			n.tr.Update()
			if s := oneLine(n.tr.LastAssistant()); s != "" {
				n.Line = s
			}
		}
	}
	n.Line = clip(n.Line, 220)

	h.mu.Lock()
	n.Unread = n.State == "done" && h.seen[id] != n.Updated
	*a = n
	h.agents[id] = a
	ready := h.ready
	h.mu.Unlock()

	if ready && h.push != nil && prev.State != n.State {
		switch n.State {
		case "need":
			h.push.Notify(id, n.Title, n.Line, "need")
		case "done":
			stamp := n.Updated
			time.AfterFunc(30*time.Second, func() { // dropped if answered on the Mac meanwhile
				h.mu.Lock()
				cur, ok := h.agents[id]
				still := ok && cur.State == "done" && cur.Updated == stamp && cur.Unread
				title, line := "", ""
				if still {
					title, line = cur.Title, cur.Line
				}
				h.mu.Unlock()
				if still {
					h.push.Notify(id, title, line, "done")
				}
			})
		}
	}
	return true
}

// descendants lists the processes started, directly or not, by pid.
func descendants(pid int) []int {
	out, err := exec.Command("ps", "-axo", "pid=,ppid=").Output()
	if err != nil {
		return nil
	}
	kids := map[int][]int{}
	for _, ln := range strings.Split(string(out), "\n") {
		var p, pp int
		if n, _ := fmt.Sscan(ln, &p, &pp); n == 2 {
			kids[pp] = append(kids[pp], p)
		}
	}
	var all []int
	for q := []int{pid}; len(q) > 0; q = q[1:] {
		all = append(all, kids[q[0]]...)
		q = append(q, kids[q[0]]...)
	}
	return all
}

func isShell(name string) bool {
	switch name {
	case "fish", "zsh", "bash", "sh", "login", "nu":
		return true
	}
	return false
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

var mdNoise = regexp.MustCompile("[*_`#>]+")

// oneLine reduces a message to its opening sentence or two, as plain text.
func oneLine(s string) string {
	for _, para := range strings.Split(s, "\n") {
		p := strings.TrimSpace(mdNoise.ReplaceAllString(para, ""))
		if p != "" {
			return clip(strings.Join(strings.Fields(p), " "), 220)
		}
	}
	return ""
}

var stateRank = map[string]int{"need": 0, "done": 1, "working": 2, "idle": 3}

// List returns the agents in display order.
func (h *Hub) List() []Agent {
	h.mu.Lock()
	out := make([]Agent, 0, len(h.agents))
	for _, a := range h.agents {
		c := *a
		if c.State == "done" && !c.Unread { // a finish you have read is just idle
			c.State = "idle"
		}
		out = append(out, c)
	}
	h.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if ri, rj := stateRank[out[i].State], stateRank[out[j].State]; ri != rj {
			return ri < rj
		}
		if out[i].Updated != out[j].Updated {
			return out[i].Updated > out[j].Updated
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (h *Hub) Get(id string) (Agent, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	a, ok := h.agents[id]
	if !ok {
		return Agent{}, false
	}
	return *a, true
}

// MarkSeen records that the user has read an agent's latest finish.
func (h *Hub) MarkSeen(id string) {
	h.mu.Lock()
	a, ok := h.agents[id]
	changed := ok && a.Unread
	if ok {
		h.seen[id] = a.Updated
		a.Unread = false
	}
	for k := range h.seen { // forget panes that are gone
		if _, live := h.agents[k]; !live {
			delete(h.seen, k)
		}
	}
	b, _ := json.Marshal(h.seen)
	h.mu.Unlock()
	if changed {
		os.WriteFile(filepath.Join(h.dir, "seen.json"), b, 0o600)
		h.broadcast()
	}
}

func (h *Hub) Subscribe() (chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}
}

func (h *Hub) broadcast() {
	b, _ := json.Marshal(h.List())
	h.mu.Lock()
	defer h.mu.Unlock()
	if string(b) == h.last {
		return
	}
	h.last = string(b)
	for ch := range h.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Nudge asks for a pane to be re-read soon, after we typed into it.
func (h *Hub) Nudge(a Agent) {
	go func() {
		for _, d := range []time.Duration{400 * time.Millisecond, 1500 * time.Millisecond} {
			time.Sleep(d)
			select {
			case h.dirty <- dirtyReq{a.Session, a.block}:
			default:
			}
		}
	}()
}

// ---- prompts read off the screen ----

type PromptOption struct {
	N           int    `json:"n"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Selected    bool   `json:"selected,omitempty"`
	cont        string // wrapped continuation of the label, or a description line
}

type Prompt struct {
	Kind    string         `json:"kind"` // permission | question | other
	Title   string         `json:"title"`
	Body    string         `json:"body,omitempty"`
	Tool    string         `json:"tool,omitempty"`
	Options []PromptOption `json:"options"`
}

var contRe = regexp.MustCompile(`^[\s│|┃]{3,}(\S.*?)[\s│|┃]*$`)

var optRe = regexp.MustCompile(`^[\s│|┃]*([❯>›▶➤●])?\s*(\d{1,2})[.)]\s+(\S.*?)[\s│|┃]*$`)

// parseOptions finds the numbered choices a terminal prompt is showing: the
// last run of lines numbered 1, 2, 3… near the bottom of the screen.
var modeRe = regexp.MustCompile(`(bypass permissions|accept edits|plan mode|auto mode|manual mode) on\b`)

// screenMode reads Claude Code's permission mode off the footer of its
// screen, or returns "" when the footer is not showing. The session file
// records the mode only when a turn starts, so a switch shows here first.
func screenMode(screen string) string {
	lines := strings.Split(screen, "\n")
	for i := len(lines) - 1; i >= 0 && i >= len(lines)-6; i-- {
		if m := modeRe.FindStringSubmatch(lines[i]); m != nil {
			return map[string]string{"bypass permissions": "bypassPermissions", "accept edits": "acceptEdits",
				"plan mode": "plan", "auto mode": "auto", "manual mode": "default"}[m[1]]
		}
	}
	return ""
}

func parseOptions(screen string) []PromptOption {
	lines := strings.Split(lastLines(screen, 45), "\n")
	var best, cur []PromptOption
	attach := false // whether the next indented line belongs to the last option
	for _, ln := range lines {
		m := optRe.FindStringSubmatch(ln)
		if m == nil {
			// An indented line right under an option continues it.
			c := contRe.FindStringSubmatch(ln)
			if attach && c != nil && !strings.ContainsAny(c[1], "─━╭╰╮╯") {
				cur[len(cur)-1].cont = strings.TrimSpace(cur[len(cur)-1].cont + " " + c[1])
			} else {
				attach = false
			}
			continue
		}
		n := 0
		for _, c := range m[2] {
			n = n*10 + int(c-'0')
		}
		o := PromptOption{N: n, Label: strings.TrimSpace(m[3]), Selected: m[1] != ""}
		switch {
		case n == 1:
			cur = []PromptOption{o}
		case n == len(cur)+1 && len(cur) > 0:
			cur = append(cur, o)
		default:
			continue
		}
		attach = true
		if len(cur) >= 2 {
			best = cur
		}
	}
	return best
}

// BuildPrompt describes what a blocked agent is asking, combining the
// pending tool call from its transcript with the choices on its screen.
func (h *Hub) BuildPrompt(a Agent) *Prompt {
	p := &Prompt{Kind: "other", Title: "Waiting for you", Body: a.Line}
	if a.Need == "permission" || a.Need == "question" {
		p.Kind = a.Need
	}
	if screen, err := h.rex.screenText(a.Session, a.block, 45); err == nil {
		p.Options = parseOptions(screen)
	}
	if a.tr != nil {
		if t := a.tr.PendingTool(); t != nil {
			if len(t.Questions) > 0 {
				p.Kind = "question"
				q := matchQuestion(t.Questions, p.Options)
				p.Title, p.Body = q.Question, ""
				// Keep the agent's own choices; the terminal's extra rows
				// ("Type something", "Chat about this") are covered by
				// the reply field.
				var own []PromptOption
				for _, o := range p.Options {
					for _, qo := range q.Options {
						if strings.EqualFold(qo.Label, o.Label) {
							o.Description = qo.Description
							own = append(own, o)
						}
					}
				}
				if len(own) > 0 {
					p.Options = own
				}
			} else {
				if p.Kind == "other" {
					p.Kind = "permission"
				}
				p.Tool = t.Name
				p.Title = "Wants to use " + t.Name
				if t.Name == "Bash" {
					p.Title = "Wants to run a command"
				}
				p.Body = firstNonEmpty(t.Input, t.Summary)
			}
		}
	}
	if p.Kind != "question" { // permission dialogs have no descriptions: the extra line is a wrapped label
		for i := range p.Options {
			if c := p.Options[i].cont; c != "" {
				p.Options[i].Label += " " + c
			}
		}
	}
	return p
}

// matchQuestion picks the question whose options are the ones on screen.
func matchQuestion(qs []Question, on []PromptOption) Question {
	best, score := qs[0], -1
	for _, q := range qs {
		s := 0
		for _, o := range on {
			for _, qo := range q.Options {
				if strings.EqualFold(qo.Label, o.Label) {
					s++
				}
			}
		}
		if s > score {
			best, score = q, s
		}
	}
	return best
}

// ---- terminal HTML ----

var (
	tagRe   = regexp.MustCompile(`<(/?)([a-zA-Z0-9]+)((?:\s+[a-zA-Z-]+="[^"<>]*")*)\s*/?>`)
	styleRe = regexp.MustCompile(`\sstyle="([^"<>]*)"`)
	safeCSS = regexp.MustCompile(`^[a-zA-Z0-9#(),.%:; \-]*$`)
)

// sanitizeTermHTML keeps only styled spans from Rex's HTML rendering.
func sanitizeTermHTML(in string) string {
	var sb strings.Builder
	depth, pos := 0, 0
	for _, m := range tagRe.FindAllStringSubmatchIndex(in, -1) {
		sb.WriteString(strings.ReplaceAll(in[pos:m[0]], "<", "&lt;"))
		pos = m[1]
		closing := in[m[2]:m[3]] == "/"
		switch strings.ToLower(in[m[4]:m[5]]) {
		case "div", "span", "pre", "b", "i", "u", "code":
		default:
			continue
		}
		if closing {
			if depth > 0 {
				sb.WriteString("</span>")
				depth--
			}
			continue
		}
		style := ""
		if s := styleRe.FindStringSubmatch(in[m[6]:m[7]]); s != nil && safeCSS.MatchString(s[1]) && !strings.Contains(strings.ToLower(s[1]), "url") {
			style = strings.ReplaceAll(s[1], "display: inline;", "")
		}
		sb.WriteString(`<span style="` + style + `">`)
		depth++
	}
	sb.WriteString(strings.ReplaceAll(in[pos:], "<", "&lt;"))
	for ; depth > 0; depth-- {
		sb.WriteString("</span>")
	}
	return sb.String()
}
