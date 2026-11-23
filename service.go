package main

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// Starting, stopping and installing the background server. On macOS the
// installed form is a launchd agent, which starts roi at login and restarts
// it if it crashes.

const launchdLabel = "local.roi"

func plistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist")
}

func launchdDomain() string { return fmt.Sprintf("gui/%d", os.Getuid()) }
func launchdTarget() string { return launchdDomain() + "/" + launchdLabel }

func launchctl(args ...string) error {
	out, err := exec.Command("launchctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl %s: %s", args[0], firstNonEmpty(strings.TrimSpace(string(out)), err.Error()))
	}
	return nil
}

func serviceInstalled() bool {
	_, err := os.Stat(plistPath())
	return runtime.GOOS == "darwin" && err == nil
}

// serviceLoaded reports whether launchd currently manages roi.
func serviceLoaded() bool {
	return runtime.GOOS == "darwin" && exec.Command("launchctl", "print", launchdTarget()).Run() == nil
}

func xmlEsc(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

func plist(exe string, serveArgs []string, p paths) string {
	var args strings.Builder
	for _, a := range append([]string{exe, "serve"}, serveArgs...) {
		fmt.Fprintf(&args, "\t\t<string>%s</string>\n", xmlEsc(a))
	}
	env := fmt.Sprintf("\t\t<key>PATH</key>\n\t\t<string>%s</string>\n", xmlEsc(os.Getenv("PATH")))
	if h := os.Getenv("ROI_HOME"); h != "" {
		env += fmt.Sprintf("\t\t<key>ROI_HOME</key>\n\t\t<string>%s</string>\n", xmlEsc(h))
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + launchdLabel + `</string>
	<key>ProgramArguments</key>
	<array>
` + args.String() + `	</array>
	<key>EnvironmentVariables</key>
	<dict>
` + env + `	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>ThrottleInterval</key>
	<integer>10</integer>
	<key>StandardOutPath</key>
	<string>` + xmlEsc(p.log) + `</string>
	<key>StandardErrorPath</key>
	<string>` + xmlEsc(p.log) + `</string>
</dict>
</plist>
`
}

// checkServeArgs validates options meant for `roi serve`.
func checkServeArgs(args []string) (*serveOpts, error) {
	fs, o := serveFlags()
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return nil, fmt.Errorf("%v (these are passed to `roi serve`; see `roi serve -h`)", err)
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if o.noTailnet && o.dev == "" {
		return nil, errors.New("-no-tailnet needs -dev")
	}
	return o, nil
}

var errNeedsLogin = errors.New("not signed in to Tailscale; run `roi login` first")

// checkSignedIn makes sure a background server would not sit waiting on a
// sign-in link nobody sees. It must only run while no server is running.
func checkSignedIn(p paths, o *serveOpts) error {
	if o.noTailnet {
		return nil
	}
	cfg, _ := loadConfig(p.dir)
	n, err := openNode(p, firstNonEmpty(o.hostname, cfg.Hostname), false, func(string, ...any) {})
	if err != nil {
		return fmt.Errorf("starting Tailscale: %v", err)
	}
	defer n.ts.Close()
	in, err := settle(context.Background(), n, 10*time.Second)
	if err == nil && in.State == "needs-login" {
		return errNeedsLogin
	}
	return nil // signed in, or still connecting (offline): the server keeps trying
}

func trimLog(p paths) {
	if st, err := os.Stat(p.log); err == nil && st.Size() > 5<<20 {
		os.Truncate(p.log, 0)
	}
}

// waitUp waits for the server to answer, then a little longer for its address.
func waitUp(p paths, exited <-chan error) (Status, error) {
	var st Status
	var err error
	died := false
	up := waitFor(25*time.Second, func() bool {
		select {
		case <-exited:
			died = true
			return true
		default:
		}
		st, err = dial(p).status()
		return err == nil
	})
	if died || !up {
		tail, _ := exec.Command("tail", "-n", "8", p.log).Output()
		return st, fmt.Errorf("the server did not start. The end of its log:\n%s", strings.TrimRight(string(tail), "\n"))
	}
	waitFor(12*time.Second, func() bool {
		st, err = dial(p).status()
		return err != nil || st.Tailscale.State != "starting" && (st.Tailscale.URL != "" || st.Tailscale.State != "running" || st.Tailscale.Problem != "")
	})
	return st, err
}

func reportUp(st Status) {
	fmt.Printf("roi is running (pid %d).\n", st.PID)
	switch {
	case st.Tailscale.URL != "":
		fmt.Println(st.Tailscale.URL)
	case st.Tailscale.State == "off":
		fmt.Printf("http://%s (this Mac only)\n", st.Dev)
	case st.Tailscale.Problem != "":
		fmt.Println("It cannot serve on the tailnet:", st.Tailscale.Problem)
	case st.Tailscale.State == "needs-login":
		fmt.Println("It is not signed in to Tailscale. Run `roi login`.")
	default:
		fmt.Println("It is still connecting to Tailscale; check `roi status` in a moment.")
	}
}

func startDetached(p paths, serveArgs []string) (Status, error) {
	exe, err := os.Executable()
	if err != nil {
		return Status{}, err
	}
	trimLog(p)
	logf, err := os.OpenFile(p.log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return Status{}, err
	}
	defer logf.Close()
	cmd := exec.Command(exe, append([]string{"serve"}, serveArgs...)...)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // survives this terminal closing
	if err := cmd.Start(); err != nil {
		return Status{}, err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	return waitUp(p, exited)
}

func cmdStart(args []string) error {
	fs := newFlags("start [SERVE OPTIONS]", "Start the server in the background. Options are those of `roi serve`. If roi is installed as a login service, this starts that service.")
	fs.SetOutput(io.Discard)
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		fs.SetOutput(os.Stdout)
		fs.Usage()
		return nil
	}
	p, err := getPaths()
	if err != nil {
		return err
	}
	if st, err := dial(p).status(); err == nil {
		fmt.Printf("roi is already running (pid %d).\n", st.PID)
		return nil
	}
	o, err := checkServeArgs(args)
	if err != nil {
		return exitErr{2, err.Error()}
	}
	if serviceInstalled() {
		if len(args) > 0 {
			return errors.New("installed as a login service, whose options are set by `roi install`")
		}
		if !serviceLoaded() {
			if err := checkSignedIn(p, o); err != nil {
				return err
			}
			trimLog(p)
			launchctl("enable", launchdTarget())
			if err := launchctl("bootstrap", launchdDomain(), plistPath()); err != nil {
				return err
			}
		} else if err := launchctl("kickstart", launchdTarget()); err != nil {
			return err
		}
		st, err := waitUp(p, nil)
		if err != nil {
			return err
		}
		reportUp(st)
		return nil
	}
	if err := checkSignedIn(p, o); err != nil {
		return err
	}
	st, err := startDetached(p, args)
	if err != nil {
		return err
	}
	reportUp(st)
	return nil
}

// stopServer stops whatever is running and reports whether anything was.
func stopServer(p paths) (bool, error) {
	c := dial(p)
	st, err := c.status()
	loaded := serviceLoaded()
	if err != nil && !loaded {
		return false, nil
	}
	if loaded && (err != nil || st.Service) {
		// Unload it, or launchd would start it again at once.
		if err := launchctl("bootout", launchdTarget()); err != nil && serviceLoaded() {
			return true, err
		}
	} else if err := c.call(context.Background(), "POST", "/stop", nil, nil); err != nil {
		return true, err
	}
	if !waitFor(20*time.Second, func() bool { _, err := c.status(); return err != nil }) {
		return true, errors.New("the server did not stop")
	}
	return true, nil
}

func cmdStop(args []string) error {
	if err := parse(newFlags("stop", "Stop the server. Agents keep running in their Rex panes."), args, 0); err != nil {
		return err
	}
	p, err := getPaths()
	if err != nil {
		return err
	}
	was, err := stopServer(p)
	if err != nil {
		return err
	}
	switch {
	case !was:
		fmt.Println("roi is not running.")
	case serviceInstalled():
		fmt.Println("Stopped. It starts again at your next login; `roi uninstall` prevents that.")
	default:
		fmt.Println("Stopped.")
	}
	return nil
}

func cmdRestart(args []string) error {
	if err := parse(newFlags("restart", "Restart the server with the options it was started with."), args, 0); err != nil {
		return err
	}
	p, err := getPaths()
	if err != nil {
		return err
	}
	st, err := dial(p).status()
	if err != nil {
		return cmdStart(nil)
	}
	if st.Service && serviceLoaded() {
		if err := launchctl("kickstart", "-k", launchdTarget()); err != nil {
			return err
		}
		old := st.PID
		if !waitFor(25*time.Second, func() bool { s, err := dial(p).status(); return err == nil && s.PID != old }) {
			return errors.New("the service did not come back; see `roi logs`")
		}
	} else {
		if _, err := stopServer(p); err != nil {
			return err
		}
		if st, err = startDetached(p, st.Args); err != nil {
			return err
		}
		reportUp(st)
		return nil
	}
	st, err = waitUp(p, nil)
	if err != nil {
		return err
	}
	reportUp(st)
	return nil
}

func cmdInstall(args []string) error {
	fs := newFlags("install [SERVE OPTIONS]", "Start roi at login and keep it running, as a launchd agent. Options are those of `roi serve` and are saved with the service; run install again to change them.")
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		fs.SetOutput(os.Stdout)
		fs.Usage()
		return nil
	}
	if runtime.GOOS != "darwin" {
		return errors.New("install is only available on macOS")
	}
	o, err := checkServeArgs(args)
	if err != nil {
		return exitErr{2, err.Error()}
	}
	p, err := getPaths()
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	// launchd starts programs with a bare PATH, so pin where rex is.
	if o.rex == "" {
		rex, err := exec.LookPath("rex")
		if err != nil {
			return errors.New("rex was not found on PATH; pass -rex /path/to/rex")
		}
		args = append(args, "-rex", rex)
	}
	if was, err := stopServer(p); err != nil {
		return err
	} else if was {
		fmt.Println("Stopped the running server.")
	}
	if err := checkSignedIn(p, o); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(plistPath()), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(plistPath(), []byte(plist(exe, args, p)), 0o644); err != nil {
		return err
	}
	trimLog(p)
	launchctl("enable", launchdTarget())
	if err := launchctl("bootstrap", launchdDomain(), plistPath()); err != nil {
		return err
	}
	st, err := waitUp(p, nil)
	if err != nil {
		return err
	}
	fmt.Println("Installed. roi now starts at login and restarts if it crashes.")
	fmt.Printf("It runs %s. After rebuilding it, run `roi restart`; after moving it, run install again.\n", exe)
	reportUp(st)
	return nil
}

func cmdUninstall(args []string) error {
	if err := parse(newFlags("uninstall", "Stop the login service and remove it. Sign-in, settings and notification subscriptions are kept."), args, 0); err != nil {
		return err
	}
	if !serviceInstalled() && !serviceLoaded() {
		fmt.Println("roi is not installed as a login service.")
		return nil
	}
	p, err := getPaths()
	if err != nil {
		return err
	}
	if serviceLoaded() {
		if err := launchctl("bootout", launchdTarget()); err != nil && serviceLoaded() {
			return err
		}
		waitFor(20*time.Second, func() bool { return !serviceLoaded() })
	}
	if err := os.Remove(plistPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	if _, err := dial(p).status(); err == nil {
		fmt.Println("Uninstalled. A server you started by hand is still running; `roi stop` stops it.")
		return nil
	}
	fmt.Println("Uninstalled. roi is stopped and no longer starts at login.")
	return nil
}

func cmdLogs(args []string) error {
	fs := newFlags("logs [-f] [-n LINES]", "Show the background server's log.")
	follow := fs.Bool("f", false, "keep printing new lines")
	n := fs.Int("n", 40, "how many lines to show")
	if err := parse(fs, args, 0); err != nil {
		return err
	}
	p, err := getPaths()
	if err != nil {
		return err
	}
	if _, err := os.Stat(p.log); err != nil {
		fmt.Println("There is no log yet. It is written by `roi start` and by the login service, not by `roi serve`.")
		return nil
	}
	a := []string{"-n", fmt.Sprint(*n)}
	if *follow {
		a = append(a, "-f")
	}
	cmd := exec.Command("tail", append(a, p.log)...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}
