package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
)

func writeLines(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestClaudeTranscript(t *testing.T) {
	p := writeLines(t,
		`{"type":"api-request-blob","message":{"role":"user","content":"ignored"}}`,
		`{"type":"user","timestamp":"t1","permissionMode":"acceptEdits","message":{"role":"user","content":"fix the bug"}}`,
		`{"type":"user","isMeta":true,"message":{"role":"user","content":[{"type":"text","text":"meta"}]}}`,
		`{"type":"user","message":{"role":"user","content":"<system-reminder>x</system-reminder>"}}`,
		`{"type":"user","message":{"role":"user","content":"<command-name>/clear</command-name><command-args>now</command-args>"}}`,
		`{"type":"assistant","isSidechain":true,"message":{"role":"assistant","content":[{"type":"text","text":"subagent chatter"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hm"},{"type":"text","text":"Looking."},{"type":"tool_use","id":"a1","name":"Bash","input":{"command":"go test ./...","description":"Run tests"}}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"a1","is_error":true,"content":[{"type":"text","text":"FAIL"}]}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"a2","name":"Edit","input":{"file_path":"/x/main.go","old_string":"a","new_string":"b"}}]}}`,
		`{"type":"user","toolUseResult":{"filePath":"/x/main.go","structuredPatch":[{"lines":[" ctx","-a","+b","+c"]}]},"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"a2","content":"ok"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"a3","name":"TodoWrite","input":{"todos":[{"content":"one","status":"completed"},{"content":"two","status":"in_progress"}]}}]}}`,
		`{"type":"permission-mode","permissionMode":"plan"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"a4","name":"AskUserQuestion","input":{"questions":[{"question":"Which?","header":"Pick","options":[{"label":"A","description":"first"},{"label":"B","description":"second"}]}]}}]}}`,
	)
	tr := newTranscript("claude", p)
	tr.Update()
	items, plan, mode, _, _ := tr.Snapshot(100)
	var kinds []string
	for _, it := range items {
		kinds = append(kinds, it.Kind)
	}
	if got, want := strings.Join(kinds, ","), "user,user,assistant,tool,tool,tool,tool"; got != want {
		t.Fatalf("kinds = %s, want %s", got, want)
	}
	if items[0].Text != "fix the bug" || items[1].Text != "/clear now" {
		t.Errorf("user texts = %q, %q", items[0].Text, items[1].Text)
	}
	bash := items[3]
	if bash.Summary != "go test ./..." || !bash.Error || bash.Pending || bash.Output != "FAIL" {
		t.Errorf("bash item = %+v", bash)
	}
	if d := items[4].Diff; d == nil || d.Added != 2 || d.Removed != 1 || d.File != "/x/main.go" {
		t.Errorf("diff = %+v", items[4].Diff)
	}
	if len(plan) != 2 || plan[1].Status != "in_progress" {
		t.Errorf("plan = %+v", plan)
	}
	if mode != "plan" {
		t.Errorf("mode = %q", mode)
	}
	pt := tr.PendingTool()
	if pt == nil || pt.Name != "AskUserQuestion" || len(pt.Questions) != 1 || pt.Questions[0].Options[1].Description != "second" {
		t.Errorf("pending = %+v", pt)
	}

	// Appending is picked up incrementally, and a half-written line waits.
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Done **now**.\n\nMore."}]}}` + "\n" + `{"type":"assistant","message":{"role":"assi`)
	f.Close()
	tr.Update()
	items, _, _, _, _ = tr.Snapshot(100)
	if len(items) != 8 || tr.LastAssistant() != "Done **now**.\n\nMore." {
		t.Fatalf("after append: %d items, last %q", len(items), tr.LastAssistant())
	}
	if got := oneLine(tr.LastAssistant()); got != "Done now." {
		t.Errorf("oneLine = %q", got)
	}
	if got, _, _, trunc, _ := tr.Snapshot(3); len(got) != 3 || !trunc {
		t.Errorf("snapshot cap: %d items, truncated %v", len(got), trunc)
	}
}

func TestCodexTranscript(t *testing.T) {
	p := writeLines(t,
		`{"type":"session_meta","payload":{"cwd":"/proj"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"rules"}]}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<turn_aborted>x"}]}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"set it up"}]}}`,
		`{"type":"response_item","payload":{"type":"custom_tool_call","call_id":"c1","name":"exec","input":"text(await tools.exec_command({cmd:\"ls -la \\\"x y\\\"\",yield_time_ms:1000}));"}}`,
		`{"type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"c1","output":"[{\"type\":\"input_text\",\"text\":\"total 0\"}]"}}`,
		`{"type":"response_item","payload":{"type":"function_call","call_id":"c2","name":"shell","arguments":"{\"command\":[\"git\",\"status\"]}"}}`,
		`{"type":"event_msg","payload":{"type":"turn_aborted"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"All set."}]}}`,
	)
	if got := codexCwd(p); got != "/proj" {
		t.Errorf("codexCwd = %q", got)
	}
	tr := newTranscript("codex", p)
	tr.Update()
	items, _, _, _, _ := tr.Snapshot(100)
	if len(items) != 5 {
		t.Fatalf("items = %+v", items)
	}
	if items[0].Text != "set it up" || items[1].Summary != `ls -la "x y"` || items[1].Output != "total 0" || items[1].Pending {
		t.Errorf("items[0..1] = %+v", items[:2])
	}
	if items[2].Summary != "git status" || !items[2].Pending || items[3].Kind != "note" || items[4].Text != "All set." {
		t.Errorf("items[2..4] = %+v", items[2:])
	}
}

func TestTailWindowSkipsPartialLine(t *testing.T) {
	big := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"` + strings.Repeat("x", 1<<20) + `"}]}}`
	lines := make([]string, 0, 30)
	for i := 0; i < 28; i++ {
		lines = append(lines, big)
	}
	lines = append(lines, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"tail"}]}}`)
	tr := newTranscript("claude", writeLines(t, lines...))
	tr.Update()
	items, _, _, trunc, _ := tr.Snapshot(1000)
	if !trunc || len(items) < 20 || len(items) >= 29 || items[len(items)-1].Text != "tail" {
		t.Fatalf("got %d items, truncated %v", len(items), trunc)
	}
}

func TestCleanUserText(t *testing.T) {
	cases := map[string][2]string{
		"hello": {"hello", "user"},
		"<system-reminder>a</system-reminder>\nreal text":                    {"real text", "user"},
		"<local-command-stdout>x</local-command-stdout>":                     {"", ""},
		"[Request interrupted by user for tool use]":                         {"Interrupted", "note"},
		"<task-notification>done</task-notification>":                        {"", ""},
		"Caveat: The messages below were generated by the user":              {"", ""},
		"<command-name>/model</command-name>\n<command-args></command-args>": {"/model", "user"},
		"a < b and c > d": {"a < b and c > d", "user"},
	}
	for in, want := range cases {
		if text, kind := cleanUserText(in); text != want[0] || kind != want[1] {
			t.Errorf("cleanUserText(%q) = %q, %q; want %q, %q", in, text, kind, want[0], want[1])
		}
	}
}

func TestParseOptions(t *testing.T) {
	perm := "● Bash(rm -rf build)\n\n 1. first list item in prose\n 2. second\n\n╭──────────────╮\n│ Bash command │\n│ Do you want to proceed?\n│ ❯ 1. Yes\n│   2. Yes, and don't ask again for rm commands in /x  │\n│   3. No, and tell Claude what to do differently (esc)\n╰──────────────╯"
	got := parseOptions(perm)
	if len(got) != 3 || got[0].Label != "Yes" || !got[0].Selected || got[1].Selected || !strings.HasPrefix(got[2].Label, "No, and tell") {
		t.Fatalf("permission options = %+v", got)
	}
	if strings.HasSuffix(got[1].Label, "│") {
		t.Errorf("box border left in label %q", got[1].Label)
	}
	ask := "Which transport?\n\n❯ 1. Embedded Tailscale\n     One process on the Mac\n  2. Relay\n     Works anywhere\n  3. Type something.\n\nEnter to select"
	if got := parseOptions(ask); len(got) != 3 || got[1].Label != "Relay" || got[1].N != 2 {
		t.Fatalf("question options = %+v", got)
	}
	wrapped := "Do you want to proceed?\n❯ 1. Yes\n  2. Yes, and always allow access to /private/tmp/roi-e2e-proj from this\n     project\n  3. No\n\n   Esc to cancel"
	if got := parseOptions(wrapped); len(got) != 3 || got[1].cont != "project" || got[2].cont != "" {
		t.Fatalf("wrapped options = %+v", got)
	}
	if got := parseOptions(ask); got[0].cont != "One process on the Mac" {
		t.Errorf("description line not kept: %+v", got[0])
	}
	if got := parseOptions("just some output\n$ ls\nfile"); got != nil {
		t.Errorf("expected no options, got %+v", got)
	}
	if got := parseOptions("1. only one item"); got != nil {
		t.Errorf("a single numbered line is not a prompt: %+v", got)
	}
}

func TestSanitizeTermHTML(t *testing.T) {
	in := `<div style="font-family: 'x'"><div style="display: inline;color: rgb(1, 2, 3);">a &lt;b&gt;</div><script>alert(1)</script><img src=x onerror=1><div style="background:url(http://e)">u</div><a href="javascript:1">l</a> 1 < 2</div></div></div>`
	out := sanitizeTermHTML(in)
	for _, bad := range []string{"<script", "<img", "url(", "<a ", "javascript:1\">", "font-family"} {
		if strings.Contains(out, bad) {
			t.Errorf("output still contains %q: %s", bad, out)
		}
	}
	if !strings.Contains(out, `<span style="color: rgb(1, 2, 3);">a &lt;b&gt;</span>`) || !strings.Contains(out, "1 &lt; 2") {
		t.Errorf("styled span or text lost: %s", out)
	}
	if strings.Count(out, "<span") != strings.Count(out, "</span>") {
		t.Errorf("unbalanced spans: %s", out)
	}
}

func TestKeyAllowList(t *testing.T) {
	for _, ok := range []string{"enter", "escape", "ctrl+c", "shift+tab", "1", "a", "alt+left", "ctrl+shift+k"} {
		if !keyRe.MatchString(ok) {
			t.Errorf("%q should be allowed", ok)
		}
	}
	for _, bad := range []string{"", "-s", "--help", "ctrl+", "enter; rm", "[KeyQ]", "foo", "ctrl+enter x"} {
		if keyRe.MatchString(bad) {
			t.Errorf("%q should be refused", bad)
		}
	}
}

func TestClipKeepsUTF8(t *testing.T) {
	s := clip(strings.Repeat("é", 10), 5)
	if !strings.HasSuffix(s, "…") || strings.ContainsRune(s, '�') || len(s) > 9 {
		t.Errorf("clip = %q", s)
	}
}

func TestPlist(t *testing.T) {
	t.Setenv("PATH", "/a b:/c&d")
	t.Setenv("ROI_HOME", "")
	out := plist("/Apps/My <roi>/roi", []string{"-rex", "/x/Rex Server.app/rex", "-awake"}, paths{log: "/l/roi.log"})
	for _, want := range []string{
		"<string>local.roi</string>",
		"<string>/Apps/My &lt;roi&gt;/roi</string>\n\t\t<string>serve</string>\n\t\t<string>-rex</string>\n\t\t<string>/x/Rex Server.app/rex</string>\n\t\t<string>-awake</string>",
		"<string>/a b:/c&amp;d</string>",
		"<key>KeepAlive</key>\n\t<true/>",
		"<string>/l/roi.log</string>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plist lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "ROI_HOME") {
		t.Error("ROI_HOME written although unset")
	}
}

func TestServeArgs(t *testing.T) {
	if _, err := checkServeArgs([]string{"-awake", "-dev", "127.0.0.1:1"}); err != nil {
		t.Errorf("valid args refused: %v", err)
	}
	for _, bad := range [][]string{{"-nope"}, {"extra"}, {"-no-tailnet"}} {
		if _, err := checkServeArgs(bad); err == nil {
			t.Errorf("%v should be refused", bad)
		}
	}
}

func TestDevices(t *testing.T) {
	p, err := newPusher(t.TempDir(), "mailto:x@example.com")
	if err != nil {
		t.Fatal(err)
	}
	sub := func(ep string) (s webpush.Subscription) { s.Endpoint = ep; return }
	p.Add(sub("https://a.example/1"), DeviceMeta{Name: "Phone", UA: "Mozilla (iPhone; CPU)", Added: time.Unix(100, 0)})
	p.Add(sub("https://b.example/2"), DeviceMeta{Name: "laptop", UA: "Mozilla (Macintosh)", Added: time.Unix(50, 0)})
	p.Add(sub("https://a.example/1"), DeviceMeta{Name: "Phone", UA: "Mozilla (iPhone; CPU)", Added: time.Unix(999, 0)}) // re-subscribe keeps its date
	ds := p.Devices()
	if len(ds) != 2 || ds[0].Kind != "Mac" || ds[1].Kind != "iPhone" || ds[1].Service != "a.example" || ds[1].Added.Unix() != 100 {
		t.Fatalf("devices = %+v", ds)
	}
	if p.Remove("nope") != 0 || p.Remove("PHONE") != 1 || p.Remove(ds[0].ID) != 1 || p.Count() != 0 {
		t.Errorf("remove by name and id failed; %d left", p.Count())
	}
	// Saved state is read back.
	p.Add(sub("https://c.example/3"), DeviceMeta{Name: "tab"})
	q, _ := newPusher(filepath.Dir(p.path), "")
	if q.Count() != 1 || q.Public != p.Public || q.Devices()[0].Name != "tab" {
		t.Errorf("reload = %+v", q.Devices())
	}
}

func TestStatusWording(t *testing.T) {
	soon := time.Now().Add(72*time.Hour + time.Minute)
	cases := map[string]tsInfo{
		"not signed in — run `roi login`\n           https://x":                               {State: "needs-login", AuthURL: "https://x"},
		"signed in as me · key does not expire":                                               {State: "running", User: "me"},
		"signed in as me · key expires " + soon.Local().Format("2006-01-02") + " (in 3 days)": {State: "running", User: "me", KeyExpiry: &soon},
		"off (-no-tailnet)": {State: "off"},
	}
	for want, in := range cases {
		if got := tailscaleLine(in); got != want {
			t.Errorf("tailscaleLine(%+v) = %q, want %q", in, got, want)
		}
	}
	if m := sleepRe.FindStringSubmatch(" powernap 1\n sleep                10 (sleep prevented by coreaudiod)\n displaysleep 5"); m == nil || m[1] != "10" {
		t.Errorf("sleep setting not parsed: %v", m)
	}
	if got := strings.Join(quoteArgs([]string{"fish", "-c", "my-claude $argv"}), " "); got != `fish -c "my-claude $argv"` {
		t.Errorf("quoteArgs = %s", got)
	}
}

func TestConfig(t *testing.T) {
	dir := t.TempDir()
	if c, err := loadConfig(dir); err != nil || c.Hostname != "roi" || len(c.Launch["claude"]) != 1 {
		t.Errorf("defaults: %+v, %v", c, err)
	}
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"launch":{"claude":["x","y"]},"hostname":"desk"}`), 0o600)
	if c, err := loadConfig(dir); err != nil || c.Hostname != "desk" || len(c.Launch["claude"]) != 2 || c.Launch["codex"][0] != "codex" {
		t.Errorf("file: %+v, %v", c, err)
	}
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"launch":`), 0o600)
	if c, err := loadConfig(dir); err == nil || c.Launch["claude"][0] != "claude" {
		t.Errorf("a broken file should be reported and defaults used: %+v, %v", c, err)
	}
}
