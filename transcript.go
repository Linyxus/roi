package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// A transcript is the structured conversation read from an agent's own
// session file: Claude Code's JSONL under ~/.claude/projects, or a Codex
// rollout under ~/.codex/sessions. It is read incrementally.

type Diff struct {
	File    string   `json:"file"`
	Added   int      `json:"added"`
	Removed int      `json:"removed"`
	Lines   []string `json:"lines"`
	More    int      `json:"more,omitempty"`
}

type Todo struct {
	Text   string `json:"text"`
	Status string `json:"status"` // pending | in_progress | completed
}

type QOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type Question struct {
	Header   string    `json:"header,omitempty"`
	Question string    `json:"question"`
	Options  []QOption `json:"options"`
	Multi    bool      `json:"multiSelect,omitempty"`
}

type Item struct {
	Kind      string     `json:"kind"` // user | assistant | tool | note
	Text      string     `json:"text,omitempty"`
	TS        string     `json:"ts,omitempty"`
	ID        string     `json:"id,omitempty"`
	Name      string     `json:"name,omitempty"`
	Summary   string     `json:"summary,omitempty"`
	Input     string     `json:"input,omitempty"`
	Output    string     `json:"output,omitempty"`
	Error     bool       `json:"error,omitempty"`
	Pending   bool       `json:"pending,omitempty"`
	Diff      *Diff      `json:"diff,omitempty"`
	Questions []Question `json:"questions,omitempty"`
	// Seq is the item's position in the transcript, which is how the phone
	// names it. Lazy marks an item sent without its input, output and diff
	// lines; the phone asks for those when the step is opened.
	Seq  int  `json:"seq"`
	Lazy bool `json:"lazy,omitempty"`

	rev int // the transcript revision that last changed this item
}

type Transcript struct {
	mu        sync.Mutex
	Path      string
	Kind      string // claude | codex
	offset    int64
	items     []Item
	byID      map[string]int
	plan      []Todo
	mode      string
	model     string // model id of the latest turn
	effort    string // its reasoning effort, where the file records one
	truncated bool
	rev       int
	epoch     int // counts the times the file was replaced and read afresh
}

const (
	tailWindow = 24 << 20 // on first read, only the last 24 MiB of a file is parsed
	maxOutput  = 6000
	maxInput   = 4000
	maxDiff    = 240
)

func newTranscript(kind, path string) *Transcript {
	return &Transcript{Kind: kind, Path: path, byID: map[string]int{}}
}

// Update reads whatever was appended since the last call.
func (t *Transcript) Update() {
	t.mu.Lock()
	defer t.mu.Unlock()
	f, err := os.Open(t.Path)
	if err != nil {
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return
	}
	if st.Size() < t.offset { // file was replaced
		t.offset, t.items, t.byID, t.plan, t.truncated = 0, nil, map[string]int{}, nil, false
		t.epoch++
	}
	if st.Size() == t.offset {
		return
	}
	skipPartial := false
	if t.offset == 0 && st.Size() > tailWindow {
		t.offset = st.Size() - tailWindow
		t.truncated = true
		skipPartial = true
	}
	if _, err := f.Seek(t.offset, io.SeekStart); err != nil {
		return
	}
	r := bufio.NewReaderSize(f, 1<<20)
	before := len(t.items)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil { // incomplete last line: leave it for next time
			break
		}
		t.offset += int64(len(line))
		if skipPartial {
			skipPartial = false
			continue
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if t.Kind == "codex" {
			t.codexLine(line)
		} else {
			t.claudeLine(line)
		}
	}
	if len(t.items) != before {
		t.rev++
	}
}

func (t *Transcript) add(it Item) {
	t.rev++
	it.rev = t.rev
	t.items = append(t.items, it)
	if it.ID != "" {
		t.byID[it.ID] = len(t.items) - 1
	}
}

// touch records that item i changed in place.
func (t *Transcript) touch(i int) {
	t.rev++
	t.items[i].rev = t.rev
}

// View is one reading of a transcript for the phone: either its last few
// items, or only the items that changed since a revision the phone has.
type View struct {
	ID        string // names this file and this reading of it
	Items     []Item
	From      int // position of the oldest item the phone holds after this
	Total     int
	Rev       int
	Delta     bool
	Plan      []Todo
	Mode      string
	Truncated bool
}

func (t *Transcript) id() string { return fnv(t.Path) + "." + itoa(t.epoch) }

// slim copies item i for sending, without the parts that stay hidden until
// a step is opened. Those are most of a session's bytes.
func (t *Transcript) slim(i int) Item {
	it := t.items[i]
	it.Seq = i
	if it.Kind == "tool" {
		it.Lazy = it.Input != "" || it.Output != "" || (it.Diff != nil && len(it.Diff.Lines) > 0)
		it.Input, it.Output = "", ""
		if it.Diff != nil {
			d := *it.Diff
			d.Lines = nil
			it.Diff = &d
		}
	}
	return it
}

// View returns the items changed after revision since, from position from
// on, when have names this same reading; otherwise the last n items.
func (t *Transcript) View(have string, since, from, n int) View {
	t.mu.Lock()
	defer t.mu.Unlock()
	v := View{ID: t.id(), Total: len(t.items), Rev: t.rev, Plan: append([]Todo(nil), t.plan...), Mode: t.mode, Truncated: t.truncated, Items: []Item{}}
	if have == v.ID && since >= 0 && since <= t.rev && from >= 0 && from <= len(t.items) {
		v.Delta, v.From = true, from
		for i := from; i < len(t.items); i++ {
			if t.items[i].rev > since {
				v.Items = append(v.Items, t.slim(i))
			}
		}
		return v
	}
	v.From = max(0, len(t.items)-n)
	for i := v.From; i < len(t.items); i++ {
		v.Items = append(v.Items, t.slim(i))
	}
	return v
}

// Before returns up to n items that come before position before.
func (t *Transcript) Before(have string, before, n int) (items []Item, from int, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if have != t.id() || before < 0 || before > len(t.items) {
		return nil, 0, false
	}
	from = max(0, before-n)
	items = []Item{}
	for i := from; i < before; i++ {
		items = append(items, t.slim(i))
	}
	return items, from, true
}

// Full returns item seq whole, with its input, output and diff.
func (t *Transcript) Full(have string, seq int) (Item, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if have != t.id() || seq < 0 || seq >= len(t.items) {
		return Item{}, false
	}
	it := t.items[seq]
	it.Seq = seq
	return it, true
}

// Snapshot returns the last max items and the derived state.
func (t *Transcript) Snapshot(max int) (items []Item, plan []Todo, mode string, truncated bool, rev int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	from := 0
	if len(t.items) > max {
		from = len(t.items) - max
		truncated = true
	}
	items = append([]Item(nil), t.items[from:]...)
	return items, append([]Todo(nil), t.plan...), t.mode, truncated || t.truncated, t.rev
}

// Model returns the model id and reasoning effort of the latest turn.
func (t *Transcript) Model() (id, effort string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.model, t.effort
}

// LastAssistant returns the text of the most recent assistant message.
func (t *Transcript) LastAssistant() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := len(t.items) - 1; i >= 0; i-- {
		if t.items[i].Kind == "assistant" {
			return t.items[i].Text
		}
		if t.items[i].Kind == "user" {
			break
		}
	}
	return ""
}

// PendingTool returns the most recent tool call that has no result yet.
func (t *Transcript) PendingTool() *Item {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := len(t.items) - 1; i >= 0 && i >= len(t.items)-12; i-- {
		if t.items[i].Kind == "tool" && t.items[i].Pending {
			it := t.items[i]
			return &it
		}
		if t.items[i].Kind == "user" {
			break
		}
	}
	return nil
}

// ---- Claude Code ----

type claudeLine struct {
	Type           string          `json:"type"`
	IsSidechain    bool            `json:"isSidechain"`
	IsMeta         bool            `json:"isMeta"`
	Timestamp      string          `json:"timestamp"`
	PermissionMode string          `json:"permissionMode"`
	Message        json.RawMessage `json:"message"`
	ToolUseResult  json.RawMessage `json:"toolUseResult"`
}

type claudeBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
}

func (t *Transcript) claudeLine(line []byte) {
	// Cheap reject for the bulky bookkeeping records.
	if bytes.Contains(line[:min(len(line), 64)], []byte(`"type":"api-request`)) {
		return
	}
	var l claudeLine
	if json.Unmarshal(line, &l) != nil {
		return
	}
	switch l.Type {
	case "permission-mode":
		if l.PermissionMode != "" && l.PermissionMode != t.mode {
			t.mode = l.PermissionMode
			t.rev++
		}
		return
	case "user", "assistant":
	default:
		return
	}
	if l.IsSidechain || len(l.Message) == 0 {
		return
	}
	var m struct {
		Content json.RawMessage `json:"content"`
		Model   string          `json:"model"`
	}
	if json.Unmarshal(l.Message, &m) != nil {
		return
	}
	if l.Type == "assistant" && m.Model != "" && !strings.HasPrefix(m.Model, "<") && m.Model != t.model {
		t.model = m.Model
		t.rev++
	}
	if l.Type == "user" && l.PermissionMode != "" {
		t.mode = l.PermissionMode
	}
	var blocks []claudeBlock
	if len(m.Content) > 0 && m.Content[0] == '"' {
		var s string
		json.Unmarshal(m.Content, &s)
		blocks = []claudeBlock{{Type: "text", Text: s}}
	} else if json.Unmarshal(m.Content, &blocks) != nil {
		return
	}
	for _, b := range blocks {
		switch {
		case l.Type == "user" && b.Type == "text":
			if l.IsMeta {
				continue
			}
			if text, kind := cleanUserText(b.Text); text != "" {
				t.add(Item{Kind: kind, Text: text, TS: l.Timestamp})
			}
		case l.Type == "user" && b.Type == "tool_result":
			i, ok := t.byID[b.ToolUseID]
			if !ok {
				continue
			}
			it := &t.items[i]
			it.Pending = false
			it.Error = b.IsError
			it.Output = clip(blockText(b.Content), maxOutput)
			if d := patchDiff(l.ToolUseResult); d != nil {
				it.Diff = d
			}
			t.touch(i)
		case l.Type == "assistant" && b.Type == "text":
			if s := strings.TrimSpace(b.Text); s != "" {
				t.add(Item{Kind: "assistant", Text: s, TS: l.Timestamp})
			}
		case l.Type == "assistant" && b.Type == "tool_use":
			it := Item{Kind: "tool", ID: b.ID, Name: b.Name, TS: l.Timestamp, Pending: true}
			var in map[string]any
			json.Unmarshal(b.Input, &in)
			it.Summary = toolSummary(b.Name, in)
			it.Input = clip(prettyInput(in), maxInput)
			switch b.Name {
			case "TodoWrite":
				var v struct {
					Todos []struct {
						Content string `json:"content"`
						Status  string `json:"status"`
					} `json:"todos"`
				}
				if json.Unmarshal(b.Input, &v) == nil {
					t.plan = t.plan[:0]
					for _, td := range v.Todos {
						t.plan = append(t.plan, Todo{td.Content, td.Status})
					}
				}
			case "AskUserQuestion":
				var v struct {
					Questions []Question `json:"questions"`
				}
				if json.Unmarshal(b.Input, &v) == nil {
					it.Questions = v.Questions
				}
			}
			t.add(it)
		}
	}
}

var (
	reminderRe = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)
	cmdNameRe  = regexp.MustCompile(`(?s)<command-name>(.*?)</command-name>`)
	cmdArgsRe  = regexp.MustCompile(`(?s)<command-args>(.*?)</command-args>`)
	tagStartRe = regexp.MustCompile(`^<[a-zA-Z][a-zA-Z0-9_-]*>`)
)

// cleanUserText turns a raw user turn into what the person actually typed.
// It returns "" for harness-generated turns.
var modelSetRe = regexp.MustCompile(`<local-command-stdout>(Set model to [^<]+)</local-command-stdout>`)

func cleanUserText(s string) (text, kind string) {
	s = strings.TrimSpace(reminderRe.ReplaceAllString(s, ""))
	if s == "" {
		return "", ""
	}
	// A model switch shows as one line saying what was set, not as a message.
	if m := modelSetRe.FindStringSubmatch(s); m != nil {
		return strings.ReplaceAll(m[1], "`", ""), "note"
	}
	if m := cmdNameRe.FindStringSubmatch(s); m != nil {
		out := strings.TrimSpace(m[1])
		if out == "/model" {
			return "", ""
		}
		if a := cmdArgsRe.FindStringSubmatch(s); a != nil && strings.TrimSpace(a[1]) != "" {
			out += " " + strings.TrimSpace(a[1])
		}
		return out, "user"
	}
	if strings.HasPrefix(s, "[Request interrupted") {
		return "Interrupted", "note"
	}
	if tagStartRe.MatchString(s) || strings.HasPrefix(s, "Caveat:") {
		return "", ""
	}
	return s, "user"
}

func blockText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		json.Unmarshal(raw, &s)
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var sb strings.Builder
	for _, p := range parts {
		if p.Type == "text" || p.Type == "input_text" || p.Type == "output_text" {
			sb.WriteString(p.Text)
		} else if p.Type == "image" {
			sb.WriteString("[image]")
		}
	}
	return sb.String()
}

func patchDiff(raw json.RawMessage) *Diff {
	if len(raw) == 0 || raw[0] != '{' {
		return nil
	}
	var v struct {
		FilePath        string `json:"filePath"`
		Type            string `json:"type"`
		Content         string `json:"content"`
		StructuredPatch []struct {
			Lines []string `json:"lines"`
		} `json:"structuredPatch"`
	}
	if json.Unmarshal(raw, &v) != nil || v.FilePath == "" {
		return nil
	}
	d := &Diff{File: v.FilePath}
	var all []string
	for i, h := range v.StructuredPatch {
		if i > 0 {
			all = append(all, "@@")
		}
		all = append(all, h.Lines...)
	}
	if len(all) == 0 && v.Type == "create" {
		for _, ln := range strings.Split(strings.TrimRight(v.Content, "\n"), "\n") {
			all = append(all, "+"+ln)
		}
	}
	if len(all) == 0 {
		return nil
	}
	for _, ln := range all {
		if strings.HasPrefix(ln, "+") {
			d.Added++
		} else if strings.HasPrefix(ln, "-") {
			d.Removed++
		}
	}
	if len(all) > maxDiff {
		d.More = len(all) - maxDiff
		all = all[:maxDiff]
	}
	for i, ln := range all {
		all[i] = clip(ln, 400)
	}
	d.Lines = all
	return d
}

func str(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func toolSummary(name string, in map[string]any) string {
	var s string
	switch name {
	case "Bash":
		s = str(in, "command")
	case "Read", "Edit", "Write", "MultiEdit", "NotebookEdit":
		s = str(in, "file_path", "notebook_path")
	case "Grep", "Glob":
		s = str(in, "pattern")
	case "Agent", "Task":
		s = str(in, "description", "prompt")
	case "WebFetch":
		s = str(in, "url")
	case "WebSearch":
		s = str(in, "query")
	case "Skill":
		s = str(in, "skill")
	case "AskUserQuestion":
		if qs, ok := in["questions"].([]any); ok && len(qs) > 0 {
			if q, ok := qs[0].(map[string]any); ok {
				s = str(q, "question")
			}
		}
	default:
		s = str(in, "description", "command", "file_path", "path", "query", "prompt", "url")
	}
	return clip(strings.Join(strings.Fields(s), " "), 300)
}

func prettyInput(in map[string]any) string {
	if in == nil {
		return ""
	}
	if c := str(in, "command"); c != "" && len(in) <= 4 {
		return c
	}
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		switch v := in[k].(type) {
		case string:
			sb.WriteString(k + ": " + v + "\n")
		default:
			b, _ := json.Marshal(v)
			sb.WriteString(k + ": " + string(b) + "\n")
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && cut > n-4 && s[cut]&0xC0 == 0x80 { // do not split a UTF-8 sequence
		cut--
	}
	return s[:cut] + "…"
}

// ---- Codex ----

var codexCmdRe = regexp.MustCompile(`cmd:\s*"((?:[^"\\]|\\.)*)"`)

func (t *Transcript) codexLine(line []byte) {
	var l struct {
		Timestamp string `json:"timestamp"`
		Type      string `json:"type"`
		Payload   struct {
			Type      string          `json:"type"`
			Role      string          `json:"role"`
			Content   json.RawMessage `json:"content"`
			Name      string          `json:"name"`
			CallID    string          `json:"call_id"`
			Input     string          `json:"input"`
			Arguments string          `json:"arguments"`
			Output    json.RawMessage `json:"output"`
			Model     string          `json:"model"`
			Effort    string          `json:"effort"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &l) != nil {
		return
	}
	p := l.Payload
	switch {
	case l.Type == "event_msg" && p.Type == "turn_aborted":
		t.add(Item{Kind: "note", Text: "Interrupted", TS: l.Timestamp})
	case l.Type == "turn_context":
		if p.Model != "" && (p.Model != t.model || p.Effort != t.effort) {
			t.model, t.effort = p.Model, p.Effort
			t.rev++
		}
	case l.Type != "response_item":
		return
	case p.Type == "message" && p.Role == "user":
		s := strings.TrimSpace(blockText(p.Content))
		if s != "" && !tagStartRe.MatchString(s) {
			t.add(Item{Kind: "user", Text: s, TS: l.Timestamp})
		}
	case p.Type == "message" && p.Role == "assistant":
		if s := strings.TrimSpace(blockText(p.Content)); s != "" {
			t.add(Item{Kind: "assistant", Text: s, TS: l.Timestamp})
		}
	case p.Type == "custom_tool_call" || p.Type == "function_call":
		in := p.Input
		if in == "" {
			in = p.Arguments
		}
		sum := in
		if m := codexCmdRe.FindStringSubmatch(in); m != nil {
			var s string
			if json.Unmarshal([]byte(`"`+m[1]+`"`), &s) == nil {
				sum = s
			}
		} else {
			var a map[string]any
			if json.Unmarshal([]byte(in), &a) == nil {
				switch c := a["command"].(type) {
				case string:
					sum = c
				case []any:
					parts := make([]string, 0, len(c))
					for _, x := range c {
						if s, ok := x.(string); ok {
							parts = append(parts, s)
						}
					}
					sum = strings.Join(parts, " ")
				default:
					sum = toolSummary("", a)
				}
			}
		}
		name := p.Name
		if name == "exec" || name == "shell" || name == "exec_command" {
			name = "Bash"
		}
		t.add(Item{Kind: "tool", ID: p.CallID, Name: name, TS: l.Timestamp, Pending: true,
			Summary: clip(strings.Join(strings.Fields(sum), " "), 300), Input: clip(in, maxInput)})
	case p.Type == "custom_tool_call_output" || p.Type == "function_call_output":
		i, ok := t.byID[p.CallID]
		if !ok {
			return
		}
		out := ""
		if len(p.Output) > 0 && p.Output[0] == '"' {
			json.Unmarshal(p.Output, &out)
			if strings.HasPrefix(out, "[") { // a JSON-encoded block list
				if s := blockText(json.RawMessage(out)); s != "" {
					out = s
				}
			}
		} else {
			out = blockText(p.Output)
		}
		t.items[i].Pending = false
		t.items[i].Output = clip(out, maxOutput)
		t.touch(i)
	}
}

// ---- locating transcripts ----

// claudeTranscriptPath maps a Claude Code process to its session file.
func claudeTranscriptPath(home string, pid int) string {
	b, err := os.ReadFile(filepath.Join(home, ".claude", "sessions", itoa(pid)+".json"))
	if err != nil {
		return ""
	}
	var v struct {
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(b, &v) != nil || v.SessionID == "" || strings.ContainsAny(v.SessionID, "/*?[") {
		return ""
	}
	m, _ := filepath.Glob(filepath.Join(home, ".claude", "projects", "*", v.SessionID+".jsonl"))
	if len(m) == 0 {
		return ""
	}
	return m[0]
}

var (
	codexMetaMu sync.Mutex
	codexMeta   = map[string]string{} // rollout path -> cwd
)

// codexTranscriptPath picks the most recently written rollout for a directory.
func codexTranscriptPath(home, cwd string) string {
	files, _ := filepath.Glob(filepath.Join(home, ".codex", "sessions", "*", "*", "*", "rollout-*.jsonl"))
	type fi struct {
		path string
		mod  int64
	}
	var list []fi
	for _, p := range files {
		if st, err := os.Stat(p); err == nil {
			list = append(list, fi{p, st.ModTime().UnixNano()})
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].mod > list[j].mod })
	if len(list) > 60 {
		list = list[:60]
	}
	codexMetaMu.Lock()
	defer codexMetaMu.Unlock()
	for _, f := range list {
		c, ok := codexMeta[f.path]
		if !ok {
			c = codexCwd(f.path)
			codexMeta[f.path] = c
		}
		if c == cwd {
			return f.path
		}
	}
	return ""
}

func codexCwd(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	for i := 0; i < 5; i++ {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			var l struct {
				Type    string `json:"type"`
				Payload struct {
					Cwd string `json:"cwd"`
				} `json:"payload"`
			}
			if json.Unmarshal(line, &l) == nil && l.Type == "session_meta" {
				return l.Payload.Cwd
			}
		}
		if err != nil {
			break
		}
	}
	return ""
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
