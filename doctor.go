package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

type check struct {
	level  string // ok | warn | fail
	name   string
	detail string
}

func cmdDoctor(args []string) error {
	if err := parse(newFlags("doctor", "Check everything roi depends on. Exits with status 1 if something is broken."), args, 0); err != nil {
		return err
	}
	p, err := getPaths()
	if err != nil {
		return err
	}
	var out []check
	add := func(level, name, format string, a ...any) {
		out = append(out, check{level, name, fmt.Sprintf(format, a...)})
	}

	// Settings.
	cfg, cerr := loadConfig(p.dir)
	if cerr != nil {
		add("fail", "Settings", "%v; roi is using its defaults", cerr)
	} else {
		add("ok", "Settings", "%s", p.dir)
	}

	// The server.
	st, serr := dial(p).status()
	running := serr == nil
	switch {
	case running && st.Service:
		add("ok", "Server", "running as a login service (pid %d)", st.PID)
	case running && serviceInstalled():
		add("warn", "Server", "running (pid %d), but not as the installed login service; `roi restart` after `roi stop` fixes that", st.PID)
	case running:
		add("warn", "Server", "running (pid %d) until this Mac restarts; `roi install` makes it start at login", st.PID)
	case serviceInstalled():
		add("fail", "Server", "installed as a login service but not running; `roi start`, then `roi logs` if it fails")
	default:
		add("fail", "Server", "not running; `roi start`, or `roi install` to start it at login")
	}

	// Rex.
	rexBin := ""
	for i, a := range st.Args {
		if (a == "-rex" || a == "--rex") && i+1 < len(st.Args) {
			rexBin = st.Args[i+1]
		}
	}
	if rexBin == "" {
		rexBin, _ = exec.LookPath("rex")
	}
	if rexBin == "" {
		add("fail", "Rex", "the rex command was not found on PATH")
	} else if ss, err := (&Rex{bin: rexBin}).sessions(); err != nil {
		add("fail", "Rex", "the Rex server does not answer: %v", err)
	} else {
		add("ok", "Rex", "server answers · %d session%s", len(ss), map[bool]string{true: "s"}[len(ss) != 1])
	}

	// Tailscale.
	in := st.Tailscale
	if !running {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		if n, err := openNode(p, cfg.Hostname, false, func(string, ...any) {}); err != nil {
			in = tsInfo{State: "starting", Problem: err.Error()}
		} else {
			in, _ = settle(ctx, n, 10*time.Second)
			if in.State == "running" && in.URL == "" && in.Problem == "" {
				waitFor(5*time.Second, func() bool { in, _ = n.Info(ctx); return in.URL != "" || in.Problem != "" })
			}
			n.ts.Close()
		}
		cancel()
	}
	switch {
	case in.State == "off":
		add("warn", "Tailscale", "off: the server was started with -no-tailnet, so no phone can reach it")
	case in.State == "needs-login":
		add("fail", "Tailscale", "not signed in; run `roi login`")
	case in.State == "needs-approval":
		add("fail", "Tailscale", "signed in, but an admin of your tailnet must approve the device")
	case in.State != "running":
		add("fail", "Tailscale", "cannot connect%s", map[bool]string{true: ": " + in.Problem}[in.Problem != ""])
	case in.Problem != "":
		add("fail", "Tailscale", "%s", in.Problem)
	default:
		add("ok", "Tailscale", "signed in as %s · %s", firstNonEmpty(in.User, "unknown"), in.URL)
		switch left := time.Duration(0); {
		case in.KeyExpiry == nil:
			add("ok", "Key expiry", "the device's key does not expire")
		default:
			left = time.Until(*in.KeyExpiry)
			level := "ok"
			advice := ""
			if left < 14*24*time.Hour {
				level, advice = "warn", "; turn off key expiry for the device in the Tailscale admin console, or run `roi logout` then `roi login`"
			}
			add(level, "Key expiry", "%s (in %s)%s", in.KeyExpiry.Local().Format("2006-01-02"), span(max(left, 0)), advice)
		}
	}

	// Launch commands.
	kinds := make([]string, 0, len(cfg.Launch))
	for k := range cfg.Launch {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	shell := loginShell()
	for _, k := range kinds {
		cmd := cfg.Launch[k]
		shown := strings.Join(quoteArgs(cmd), " ")
		if missing := unresolved(shell, cmd); missing != "" {
			add("fail", "Start "+k, "%s — %q is not a command in your login shell (%s)", shown, missing, shell)
		} else {
			add("ok", "Start "+k, "%s", shown)
		}
	}

	// Sleep.
	if runtime.GOOS == "darwin" {
		level, detail := sleepCheck(st.Awake)
		add(level, "Sleep", "%s", detail)
	}

	// Notifications.
	devices := st.Devices
	if !running {
		if push, err := newPusher(p.dir, ""); err == nil {
			devices = push.Count()
		}
	}
	if devices == 0 {
		add("warn", "Notifications", "no phone is subscribed; in roi on the phone, tap \"Mac online\", then \"Turn on notifications\"")
	} else {
		add("ok", "Notifications", "%d device%s subscribed", devices, map[bool]string{true: "s"}[devices != 1])
	}

	width, bad, warned := 0, 0, 0
	for _, c := range out {
		width = max(width, len(c.name))
	}
	for _, c := range out {
		fmt.Printf("%-4s  %-*s  %s\n", c.level, width, c.name, c.detail)
		switch c.level {
		case "fail":
			bad++
		case "warn":
			warned++
		}
	}
	fmt.Println()
	if bad > 0 {
		return exitErr{1, fmt.Sprintf("%d problem%s found", bad, map[bool]string{true: "s"}[bad != 1])}
	}
	if warned > 0 {
		fmt.Printf("roi works; %d thing%s could be better.\n", warned, map[bool]string{true: "s"}[warned != 1])
	} else {
		fmt.Println("Everything roi depends on is in order.")
	}
	return nil
}

// loginShell is the shell Rex runs launch commands through.
func loginShell() string {
	if runtime.GOOS == "darwin" {
		if u, err := user.Current(); err == nil {
			out, _ := exec.Command("dscl", ".", "-read", "/Users/"+u.Username, "UserShell").Output()
			if f := strings.Fields(string(out)); len(f) == 2 {
				return f[1]
			}
		}
	}
	return firstNonEmpty(os.Getenv("SHELL"), "/bin/sh")
}

var wordRe = regexp.MustCompile(`^[A-Za-z0-9_./+-]+$`)

func quoteArgs(a []string) []string {
	out := make([]string, len(a))
	for i, s := range a {
		if wordRe.MatchString(s) {
			out[i] = s
		} else {
			out[i] = strconv.Quote(s)
		}
	}
	return out
}

// unresolved names the first command word the login shell cannot find, or
// "" when the launch command resolves. For `SHELL -c "word …"` it also
// checks the word, which is how a shell function or alias is launched.
func unresolved(shell string, cmd []string) string {
	has := func(word string, via ...string) bool {
		if !wordRe.MatchString(word) {
			return true // cannot check; do not cry wolf
		}
		probe := "type " + word + " >/dev/null 2>&1"
		if len(via) > 0 {
			probe = via[0] + " -c '" + probe + "'"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, shell, "-l", "-c", probe).Run() == nil
	}
	if len(cmd) == 0 {
		return "(empty)"
	}
	if !has(cmd[0]) {
		return cmd[0]
	}
	if len(cmd) >= 3 && cmd[1] == "-c" && wordRe.MatchString(cmd[0]) {
		if f := strings.Fields(cmd[2]); len(f) > 0 && !has(f[0], cmd[0]) {
			return f[0]
		}
	}
	return ""
}

var sleepRe = regexp.MustCompile(`(?m)^\s*sleep\s+(\d+)(.*)$`)

// sleepCheck reports whether the Mac will idle-sleep under agents.
func sleepCheck(awake string) (level, detail string) {
	out, err := exec.Command("pmset", "-g").Output()
	m := sleepRe.FindStringSubmatch(string(out))
	if err != nil || m == nil {
		return "warn", "could not read the Mac's sleep setting"
	}
	batt, _ := exec.Command("pmset", "-g", "batt").Output()
	source := "battery"
	if strings.Contains(string(batt), "AC Power") {
		source = "power"
	}
	lid := " Closing the lid still sleeps it unless it is on power with an external display."
	switch {
	case m[1] == "0":
		return "ok", "the Mac does not idle-sleep on " + source + "." + lid
	case awake == "holding" || awake == "idle":
		return "ok", "the Mac idle-sleeps after " + m[1] + " min on " + source + ", but roi keeps it awake while an agent works (-awake)." + lid
	case strings.Contains(m[2], "sleep prevented by"):
		return "warn", "the Mac idle-sleeps after " + m[1] + " min on " + source + "; another program is keeping it awake right now. `roi install -awake` makes roi do so while an agent works." + lid
	}
	return "warn", "the Mac idle-sleeps after " + m[1] + " min on " + source + ", which stops agents and roi. `roi install -awake` keeps it awake while an agent works." + lid
}
