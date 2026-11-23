package main

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Switching an agent's model. Neither Claude Code nor Codex has a command
// that sets the model for one session: both have a /model picker, in which
// Enter saves the choice as the default for every later session and "s"
// applies it to this session only. roi drives that picker, reads it off the
// screen, and only ever confirms with "s".

// PickRow is one numbered line of a picker: a model or a reasoning level.
type PickRow struct {
	N       int    `json:"n"`
	Label   string `json:"label"`
	Desc    string `json:"desc,omitempty"`
	Current bool   `json:"current,omitempty"`
	Default bool   `json:"default,omitempty"`

	cursor bool
	more   bool // opens a further list, which roi does not follow
}

type pickerView struct {
	reasoning bool // the reasoning-level list, not the model list
	rows      []PickRow
	scrolls   bool   // more rows than fit on the screen
	effort    string // Claude Code shows the effort on the model list
	effortDef bool
	hasEffort bool
}

var (
	pickTitleRe  = regexp.MustCompile(`(?i)^\s*Select (model|reasoning level)`)
	pickRowRe    = regexp.MustCompile(`^\s*(?:([❯›>])\s+)?(\d+)\.\s+(\S.*)$`)
	pickEffortRe = regexp.MustCompile(`^\s*\S\s+(\S.*?) effort( \(default\))?\s+←/→`)
	pickFootRe   = regexp.MustCompile(`(?i)\besc (to cancel|back)\b`)
	pickMoreRe   = regexp.MustCompile(`^\s*[↑↓]( \d+ more)?\s*$`)
	pickGapRe    = regexp.MustCompile(`\s{2,}`)
	pickPromptRe = regexp.MustCompile(`^\s*[❯›>](\s|$)`)
	pickInputRe  = regexp.MustCompile(`^\s*[❯›>]\s*/model(\s{2,}.*)?$`)
	claudeSetRe  = regexp.MustCompile(`Set model to (.+?) for this session only`)
	codexSetRe   = regexp.MustCompile(`Model changed to (.+?) for this session only`)
)

// parsePicker reads an open picker off the end of a screen, or returns nil
// when none is open.
func parsePicker(screen string) *pickerView {
	lines := strings.Split(strings.ReplaceAll(screen, " ", " "), "\n")
	at := -1
	for i, l := range lines {
		if pickTitleRe.MatchString(l) {
			at = i
		}
	}
	if at < 0 {
		return nil
	}
	v := &pickerView{reasoning: strings.Contains(strings.ToLower(lines[at]), "reasoning")}
	closed := false
	for _, l := range lines[at+1:] {
		if pickFootRe.MatchString(l) {
			closed = true
			break
		}
		if pickMoreRe.MatchString(l) {
			v.scrolls = true
			continue
		}
		if m := pickEffortRe.FindStringSubmatch(l); m != nil {
			v.effort, v.effortDef, v.hasEffort = m[1], m[2] != "", true
			continue
		}
		if m := pickRowRe.FindStringSubmatch(l); m != nil {
			n, _ := strconv.Atoi(m[2])
			parts := pickGapRe.Split(strings.TrimSpace(m[3]), 2)
			r := PickRow{N: n, cursor: m[1] != ""}
			label := parts[0]
			r.Current = strings.Contains(label, "✔") || strings.Contains(label, "(current)")
			r.Default = strings.Contains(label, "(default)") || strings.Contains(label, "(recommended)")
			for _, tag := range []string{"✔", "(current)", "(default)", "(recommended)"} {
				label = strings.ReplaceAll(label, tag, "")
			}
			r.Label = strings.TrimSpace(label)
			r.more = strings.HasSuffix(r.Label, "…") || strings.HasSuffix(r.Label, "...")
			if len(parts) > 1 {
				r.Desc = strings.TrimSpace(parts[1])
			}
			v.rows = append(v.rows, r)
			continue
		}
		// A wrapped description continues on an indented line.
		if t := strings.TrimSpace(l); t != "" && len(v.rows) > 0 && strings.HasPrefix(l, "      ") {
			last := &v.rows[len(v.rows)-1]
			last.Desc = strings.TrimSpace(last.Desc + " " + t)
		}
	}
	if !closed || len(v.rows) == 0 {
		return nil
	}
	return v
}

func (v *pickerView) cursor() int {
	for _, r := range v.rows {
		if r.cursor {
			return r.N
		}
	}
	return 0
}

func (v *pickerView) row(n int) (PickRow, bool) {
	for _, r := range v.rows {
		if r.N == n {
			return r, true
		}
	}
	return PickRow{}, false
}

// modelName turns a model id from a session file into a display name:
// "provider/claude-fable-5-2" becomes "Fable 5.2", "gpt-6.1-sol" "GPT-6.1-Sol".
func modelName(id string) string {
	id = id[strings.LastIndex(id, "/")+1:]
	if id == "" || strings.HasPrefix(id, "<") {
		return ""
	}
	title := func(s string) string {
		if s == "gpt" {
			return "GPT"
		}
		return strings.ToUpper(s[:1]) + s[1:]
	}
	parts := strings.FieldsFunc(id, func(r rune) bool { return r == '-' })
	if rest, ok := strings.CutPrefix(id, "claude-"); ok {
		var words, nums []string
		for _, p := range strings.Split(rest, "-") {
			if _, err := strconv.Atoi(p); err != nil {
				words = append(words, title(p))
			} else if len(p) < 6 { // longer numbers are release dates
				nums = append(nums, p)
			}
		}
		return strings.TrimSpace(strings.Join(words, " ") + " " + strings.Join(nums, "."))
	}
	for i, p := range parts {
		parts[i] = title(p)
	}
	return strings.Join(parts, "-")
}

// rowName is what a model row is called once chosen. Claude Code's rows are
// aliases ("Opus") whose description starts with the full name ("Opus 5.5").
func rowName(kind string, r PickRow) string {
	if kind == "claude" {
		if first, _, ok := strings.Cut(r.Desc, " · "); ok && !strings.HasPrefix(first, "Use ") {
			return first
		}
	}
	return r.Label
}

// ---- driving the picker ----

var (
	errInputBusy = errors.New("the agent's input box has text in it; send or clear that first")
	errNoPicker  = errors.New("the agent did not open its model list; try again in a moment")
)

type pickDriver struct {
	s *Server
	a Agent
}

func (p pickDriver) screen() string {
	scr, _ := p.s.rex.screenText(p.a.Session, p.a.block, 44)
	return strings.ReplaceAll(scr, " ", " ")
}

func (p pickDriver) keys(k ...string) error { return p.s.rex.keys(p.a.Session, p.a.block, k...) }

// wait polls the screen until ok accepts it, and returns the last screen read.
func (p pickDriver) wait(d time.Duration, ok func(v *pickerView, scr string) bool) (*pickerView, string, bool) {
	end := time.Now().Add(d)
	for {
		scr := p.screen()
		v := parsePicker(scr)
		if ok(v, scr) {
			return v, scr, true
		}
		if time.Now().After(end) {
			return v, scr, false
		}
		time.Sleep(70 * time.Millisecond)
	}
}

// close leaves any open picker without choosing anything.
func (p pickDriver) close() {
	for i := 0; i < 4; i++ {
		if parsePicker(p.screen()) == nil {
			return
		}
		p.keys("escape")
		time.Sleep(250 * time.Millisecond)
	}
}

// open types /model and returns the model list. It refuses when the agent's
// input box already holds text, which the command would otherwise be glued to.
func (p pickDriver) open() (*pickerView, error) {
	p.close()
	if err := p.s.rex.paste(p.a.Session, p.a.block, "/model"); err != nil {
		return nil, err
	}
	// The input box is the last prompt line on the screen; earlier ones are
	// the echo of past commands.
	_, scr, ok := p.wait(2*time.Second, func(_ *pickerView, scr string) bool {
		lines := strings.Split(scr, "\n")
		for i := len(lines) - 1; i >= 0; i-- {
			if pickPromptRe.MatchString(lines[i]) {
				return pickInputRe.MatchString(lines[i])
			}
		}
		return false
	})
	if !ok {
		for _, l := range strings.Split(scr, "\n") {
			if strings.HasSuffix(strings.TrimSpace(l), "/model") { // take back what was typed
				p.keys("backspace", "backspace", "backspace", "backspace", "backspace", "backspace")
				break
			}
		}
		return nil, errInputBusy
	}
	if err := p.keys("enter"); err != nil {
		return nil, err
	}
	v, _, ok := p.wait(5*time.Second, func(v *pickerView, _ string) bool { return v != nil && !v.reasoning })
	if !ok {
		p.close()
		return nil, errNoPicker
	}
	return v, nil
}

// step presses one key and waits for what get reads to change.
func (p pickDriver) step(key string, v *pickerView, get func(*pickerView) string) (*pickerView, bool) {
	before := get(v)
	if p.keys(key) != nil {
		return v, false
	}
	nv, _, ok := p.wait(600*time.Millisecond, func(nv *pickerView, _ string) bool { return nv != nil && get(nv) != before })
	if !ok || nv == nil {
		return v, false
	}
	return nv, true
}

// all returns every row of the list, scrolling it when it does not fit.
func (p pickDriver) all(v *pickerView) []PickRow {
	rows := map[int]PickRow{}
	add := func(v *pickerView) {
		for _, r := range v.rows {
			old := rows[r.N]
			r.Current, r.Default = r.Current || old.Current, r.Default || old.Default
			if len(old.Desc) > len(r.Desc) { // a row at the edge of the window can be cut short
				r.Desc = old.Desc
			}
			rows[r.N] = r
		}
	}
	add(v)
	pos := func(v *pickerView) string { return strconv.Itoa(v.cursor()) }
	if v.scrolls {
		for _, key := range []string{"down", "up"} {
			seen := map[int]bool{v.cursor(): true}
			for i := 0; i < 60; i++ {
				nv, moved := p.step(key, v, pos)
				if !moved {
					break
				}
				v = nv
				add(v)
				if seen[v.cursor()] { // wrapped round
					break
				}
				seen[v.cursor()] = true
			}
			if _, ok := rows[1]; ok && key == "down" {
				contiguous := true
				for n := 1; n <= len(rows); n++ {
					if _, ok := rows[n]; !ok {
						contiguous = false
					}
				}
				if contiguous {
					break
				}
			}
		}
	}
	out := make([]PickRow, 0, len(rows))
	for _, r := range rows {
		if !r.more {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].N < out[j].N })
	return out
}

// moveTo puts the cursor on row n.
func (p pickDriver) moveTo(n int) (*pickerView, error) {
	v := parsePicker(p.screen())
	if v == nil || v.cursor() == 0 {
		return nil, errNoPicker
	}
	c := v.cursor()
	if c == n {
		return v, nil
	}
	key, count := "down", n-c
	if count < 0 {
		key, count = "up", -count
	}
	if count > 60 {
		return nil, errors.New("no such row")
	}
	ks := make([]string, count)
	for i := range ks {
		ks[i] = key
	}
	if err := p.keys(ks...); err != nil {
		return nil, err
	}
	nv, _, ok := p.wait(1500*time.Millisecond, func(nv *pickerView, _ string) bool { return nv != nil && nv.cursor() == n })
	if !ok {
		return nil, errors.New("the agent's list did not move as expected")
	}
	return nv, nil
}

// onModel moves to model n and checks it is still the model the phone showed.
func (p pickDriver) onModel(n int, label string) (*pickerView, error) {
	v, err := p.moveTo(n)
	if err != nil {
		return nil, err
	}
	if r, _ := v.row(n); r.Label != label {
		return nil, errors.New("the agent's model list has changed; open the menu again")
	}
	return v, nil
}

// claudeEfforts lists the effort levels of the model under the cursor by
// stepping the ←/→ control from one end to the other.
func (p pickDriver) claudeEfforts(v *pickerView) []PickRow {
	if !v.hasEffort {
		return nil
	}
	start := v.effort
	eff := func(v *pickerView) string { return v.effort }
	seen := map[string]bool{v.effort: true}
	for i := 0; i < 10; i++ {
		nv, moved := p.step("left", v, eff)
		if !moved || seen[nv.effort] {
			break
		}
		v, seen[nv.effort] = nv, true
	}
	out := []PickRow{{N: 1, Label: v.effort, Default: v.effortDef, Current: v.effort == start}}
	listed := map[string]bool{v.effort: true}
	for i := 0; i < 12; i++ {
		nv, moved := p.step("right", v, eff)
		if !moved || listed[nv.effort] {
			break
		}
		v, listed[nv.effort] = nv, true
		out = append(out, PickRow{N: len(out) + 1, Label: v.effort, Default: v.effortDef, Current: v.effort == start})
	}
	return out
}

// claudeSetEffort steps the ←/→ control to the level asked for.
func (p pickDriver) claudeSetEffort(v *pickerView, want string) error {
	eff := func(v *pickerView) string { return v.effort }
	for _, key := range []string{"left", "right"} {
		for i := 0; i < 12 && !strings.EqualFold(v.effort, want); i++ {
			nv, moved := p.step(key, v, eff)
			if !moved {
				break
			}
			v = nv
		}
	}
	if !strings.EqualFold(v.effort, want) {
		return fmt.Errorf("this model has no %q effort level", want)
	}
	return nil
}

// codexLevels opens the reasoning levels of the model under the cursor. It
// returns nil when Codex applied the model at once instead.
func (p pickDriver) codexLevels() (*pickerView, string, error) {
	if err := p.keys("enter"); err != nil {
		return nil, "", err
	}
	v, scr, ok := p.wait(4*time.Second, func(v *pickerView, _ string) bool { return v == nil || v.reasoning })
	if !ok {
		return nil, scr, errNoPicker
	}
	return v, scr, nil
}

// switchAsk is Claude Code's "Switch model?" (or "Change effort level?")
// question, shown after a pick when the conversation is cached for the
// current model: switching re-reads the whole history once.
type switchAsk struct {
	yes    int  // the number of the "Yes, switch to …" row, 0 when unnumbered
	cursor bool // the cursor is on that row
}

var (
	askTitleRe = regexp.MustCompile(`^\s*(Switch model|Change effort level)\?\s*$`)
	askYesRe   = regexp.MustCompile(`^\s*(?:([❯›>])\s*)?(?:(\d+)\.\s+)?Yes, switch to \S`)
)

func parseSwitchAsk(screen string) *switchAsk {
	lines := strings.Split(screen, "\n")
	at := -1
	for i, l := range lines {
		if askTitleRe.MatchString(l) {
			at = i
		}
	}
	if at < 0 {
		return nil
	}
	for _, l := range lines[at+1:] {
		if m := askYesRe.FindStringSubmatch(l); m != nil {
			n, _ := strconv.Atoi(m[2])
			return &switchAsk{yes: n, cursor: m[1] != ""}
		}
	}
	return nil
}

// confirmSwitch answers yes to the switch question.
func (p pickDriver) confirmSwitch(a *switchAsk) error {
	switch {
	case a.cursor:
		return p.keys("enter")
	case a.yes > 0:
		return p.keys(strconv.Itoa(a.yes))
	}
	// Unnumbered, with the cursor on "No": Claude Code does this when the
	// switch would spend usage credits. Leave that choice to the user.
	return errors.New("the agent wants you to confirm this switch in its terminal")
}

// sinceCommand is the part of a screen after the last /model typed, so a
// result line left by an earlier switch is not taken for this one's.
func sinceCommand(screen string) string {
	lines := strings.Split(screen, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if pickInputRe.MatchString(lines[i]) {
			return strings.Join(lines[i+1:], "\n")
		}
	}
	return screen
}

var errSavedDefault = errors.New("Codex applied this model at once, as its default for new sessions too. To undo that, pick your usual model in Codex's own /model list")

// ---- what roi knows about an agent's model ----

type modelState struct {
	rows   []PickRow
	read   time.Time
	name   string // set by a switch made here
	effort string
	trID   string // the session file's model id when that switch was made
}

func (s *Server) modelState(id string) modelState {
	if v, ok := s.picked.Load(id); ok {
		return v.(modelState)
	}
	return modelState{}
}

// currentModel is the model to show for an agent: a switch made here, until
// the session file reports a different model than it did at that moment.
func (s *Server) currentModel(a Agent) map[string]string {
	trID, trEffort := "", ""
	if a.tr != nil {
		trID, trEffort = a.tr.Model()
	}
	st := s.modelState(a.ID)
	if st.name != "" && st.trID == trID {
		return map[string]string{"name": st.name, "effort": strings.ToLower(st.effort)}
	}
	if st.name != "" && modelName(trID) == st.name {
		return map[string]string{"name": st.name, "effort": strings.ToLower(firstNonEmpty(trEffort, st.effort))}
	}
	if n := modelName(trID); n != "" {
		return map[string]string{"name": n, "effort": trEffort}
	}
	return nil
}

func (s *Server) modelRoutes(mux *http.ServeMux, agent func(func(http.ResponseWriter, *http.Request, Agent)) http.HandlerFunc) {
	// ready checks the agent can take a /model command right now.
	ready := func(w http.ResponseWriter, a Agent) bool {
		if a.State == "working" || a.State == "need" {
			fail(w, 409, errors.New("the model can be changed once the agent is idle"))
			return false
		}
		return true
	}

	mux.HandleFunc("GET /api/agents/{id}/models", agent(func(w http.ResponseWriter, r *http.Request, a Agent) {
		st := s.modelState(a.ID)
		if len(st.rows) == 0 || time.Since(st.read) > 5*time.Minute || r.URL.Query().Get("fresh") != "" {
			if !ready(w, a) {
				return
			}
			s.modeMu.Lock()
			p := pickDriver{s, a}
			v, err := p.open()
			var rows []PickRow
			if err == nil {
				rows = p.all(v)
				p.close()
			}
			s.modeMu.Unlock()
			if err != nil {
				fail(w, 409, err)
				return
			}
			st = s.modelState(a.ID)
			st.rows, st.read = rows, time.Now()
			// The list marks the model in use, which the session file does
			// not record until a turn has run.
			for _, row := range rows {
				if n := rowName(a.Kind, row); row.Current && n != st.name {
					st.name, st.effort, st.trID = n, "", ""
					if a.tr != nil {
						st.trID, _ = a.tr.Model()
					}
				}
			}
			s.picked.Store(a.ID, st)
		}
		writeJSON(w, map[string]any{"models": st.rows})
	}))

	mux.HandleFunc("GET /api/agents/{id}/efforts", agent(func(w http.ResponseWriter, r *http.Request, a Agent) {
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))
		label := r.URL.Query().Get("label")
		key := a.Kind + "|" + label
		if c, ok := s.efforts.Load(key); ok {
			writeJSON(w, map[string]any{"efforts": c})
			return
		}
		if !ready(w, a) {
			return
		}
		s.modeMu.Lock()
		defer s.modeMu.Unlock()
		p := pickDriver{s, a}
		defer p.close()
		if _, err := p.open(); err != nil {
			fail(w, 409, err)
			return
		}
		v, err := p.onModel(n, label)
		if err != nil {
			fail(w, 409, err)
			return
		}
		efforts := []PickRow{}
		if a.Kind == "codex" {
			lv, _, err := p.codexLevels()
			if err != nil {
				fail(w, 502, err)
				return
			}
			if lv == nil {
				fail(w, 409, errSavedDefault)
				return
			}
			efforts = p.all(lv)
			for i := range efforts { // the level Codex would pick is under its cursor
				efforts[i].Current = efforts[i].Current || efforts[i].N == lv.cursor()
			}
		} else if e := p.claudeEfforts(v); e != nil {
			efforts = e
		}
		s.efforts.Store(key, efforts)
		writeJSON(w, map[string]any{"efforts": efforts})
	}))

	mux.HandleFunc("POST /api/agents/{id}/model", agent(func(w http.ResponseWriter, r *http.Request, a Agent) {
		var b struct {
			N      int    `json:"n"`
			Label  string `json:"label"`
			Effort string `json:"effort"`
		}
		if err := readBody(r, &b); err != nil || b.N < 1 || b.Label == "" {
			fail(w, 400, errors.New("no model given"))
			return
		}
		if !ready(w, a) {
			return
		}
		s.modeMu.Lock()
		defer s.modeMu.Unlock()
		p := pickDriver{s, a}
		defer p.close()
		if _, err := p.open(); err != nil {
			fail(w, 409, err)
			return
		}
		v, err := p.onModel(b.N, b.Label)
		if err != nil {
			fail(w, 409, err)
			return
		}
		row, _ := v.row(b.N)
		name, effort, setRe := rowName(a.Kind, row), b.Effort, claudeSetRe
		if a.Kind == "codex" {
			setRe = codexSetRe
			lv, _, err := p.codexLevels()
			if err != nil {
				fail(w, 502, err)
				return
			}
			if lv == nil {
				fail(w, 409, errSavedDefault)
				return
			}
			target := lv.cursor()
			for _, lr := range p.all(lv) {
				if b.Effort != "" && strings.EqualFold(lr.Label, b.Effort) {
					target = lr.N
				}
			}
			lv, err = p.moveTo(target)
			if err != nil {
				fail(w, 502, err)
				return
			}
			lr, _ := lv.row(target)
			effort = lr.Label
		} else if v.hasEffort && b.Effort != "" {
			if err := p.claudeSetEffort(v, b.Effort); err != nil {
				fail(w, 409, err)
				return
			}
		} else if !v.hasEffort {
			effort = ""
		}
		// "s" applies to this session only. Enter would save a new default.
		if err := p.keys("s"); err != nil {
			fail(w, 502, err)
			return
		}
		settled := func(v *pickerView, scr string) bool {
			return v == nil && (setRe.MatchString(sinceCommand(scr)) || parseSwitchAsk(scr) != nil)
		}
		_, scr, _ := p.wait(5*time.Second, settled)
		// Claude Code asks once more when the conversation is cached for the
		// current model. The phone asked for the switch, so roi says yes.
		if ask := parseSwitchAsk(scr); ask != nil {
			if err := p.confirmSwitch(ask); err != nil {
				fail(w, 502, err)
				return
			}
			_, scr, _ = p.wait(5*time.Second, func(v *pickerView, scr string) bool {
				return v == nil && parseSwitchAsk(scr) == nil && setRe.MatchString(sinceCommand(scr))
			})
		}
		m := setRe.FindAllStringSubmatch(sinceCommand(scr), -1)
		if parsePicker(scr) != nil || parseSwitchAsk(scr) != nil || m == nil {
			fail(w, 502, errors.New("the agent did not confirm the switch; check its terminal"))
			return
		}
		if a.Kind == "claude" {
			name = m[len(m)-1][1]
		}
		st := s.modelState(a.ID)
		st.name, st.effort, st.trID = name, effort, ""
		if a.tr != nil {
			st.trID, _ = a.tr.Model()
		}
		for i := range st.rows {
			st.rows[i].Current = st.rows[i].N == b.N
		}
		s.picked.Store(a.ID, st)
		s.hub.Nudge(a)
		writeJSON(w, map[string]string{"name": name, "effort": strings.ToLower(effort)})
	}))
}
