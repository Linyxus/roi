package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"text/tabwriter"
	"time"

	qrcode "github.com/skip2/go-qrcode"
	"golang.org/x/term"
)

// newFlags makes a flag set for one command. usage is the line after "roi".
func newFlags(usage, about string) *flag.FlagSet {
	fs := flag.NewFlagSet(usage, flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "%s\n\nUsage:\n  roi %s\n", about, usage)
		n := 0
		fs.VisitAll(func(*flag.Flag) { n++ })
		if n > 0 {
			fmt.Fprint(fs.Output(), "\nOptions:\n")
			fs.PrintDefaults()
		}
	}
	return fs
}

// parse reports whether the command should go on; -h has already printed.
func parse(fs *flag.FlagSet, args []string, maxArgs int) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitErr{0, ""}
		}
		return exitErr{2, ""}
	}
	if maxArgs >= 0 && fs.NArg() > maxArgs {
		return exitErr{2, fmt.Sprintf("unexpected argument %q", fs.Arg(maxArgs))}
	}
	return nil
}

func waitFor(d time.Duration, fn func() bool) bool {
	for end := time.Now().Add(d); ; {
		if fn() {
			return true
		}
		if time.Now().After(end) {
			return false
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func span(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%d days", int(d.Hours()/24))
}

func isTTY(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }

// ---- status, url, pair ----

func tailscaleLine(in tsInfo) string {
	switch in.State {
	case "off":
		return "off (-no-tailnet)"
	case "needs-login":
		s := "not signed in — run `roi login`"
		if in.AuthURL != "" {
			s += "\n           " + in.AuthURL
		}
		return s
	case "needs-approval":
		return "waiting for an admin of your tailnet to approve the device"
	case "starting":
		return "connecting…"
	}
	s := "signed in"
	if in.User != "" {
		s += " as " + in.User
	}
	if in.KeyExpiry == nil {
		s += " · key does not expire"
	} else if left := time.Until(*in.KeyExpiry); left > 0 {
		s += fmt.Sprintf(" · key expires %s (in %s)", in.KeyExpiry.Local().Format("2006-01-02"), span(left))
	} else {
		s += " · key has expired"
	}
	return s
}

func cmdStatus(args []string) error {
	fs := newFlags("status [--json]", "Show whether roi is running and reachable. Exits with status 3 when it is not running.")
	asJSON := fs.Bool("json", false, "print the status as JSON")
	if err := parse(fs, args, 0); err != nil {
		return err
	}
	p, err := getPaths()
	if err != nil {
		return err
	}
	st, err := dial(p).status()
	if err != nil {
		if *asJSON {
			fmt.Println(`{"running":false}`)
		} else {
			fmt.Println("roi is not running.")
			if serviceInstalled() {
				fmt.Println("It is installed to start at login. Start it now with: roi start")
			} else {
				fmt.Println("Start it with: roi start")
			}
		}
		return exitErr{3, ""}
	}
	if *asJSON {
		b, _ := json.MarshalIndent(struct {
			Running bool `json:"running"`
			Status
		}{true, st}, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	how := "background"
	if st.Service {
		how = "login service"
	} else if st.Foreground {
		how = "foreground"
	}
	fmt.Printf("roi %s is running · pid %d · %s · up %s\n\n", st.Version, st.PID, how, span(time.Since(st.Started)))
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if st.Tailscale.URL != "" {
		fmt.Fprintf(w, "URL\t%s\n", st.Tailscale.URL)
	}
	fmt.Fprintf(w, "Tailscale\t%s\n", tailscaleLine(st.Tailscale))
	if st.Tailscale.Problem != "" {
		fmt.Fprintf(w, "Problem\t%s\n", st.Tailscale.Problem)
	}
	a := st.Agents
	fmt.Fprintf(w, "Agents\t%d waiting · %d finished · %d working · %d idle\n", a["need"], a["done"], a["working"], a["idle"])
	fmt.Fprintf(w, "Phones\t%d subscribed to notifications\n", st.Devices)
	switch st.Awake {
	case "holding":
		fmt.Fprintf(w, "Keep awake\ton · holding the Mac awake now\n")
	case "idle":
		fmt.Fprintf(w, "Keep awake\ton · no agent is working\n")
	}
	if st.Dev != "" {
		fmt.Fprintf(w, "Local\thttp://%s (no sign-in, this Mac only)\n", st.Dev)
	}
	return w.Flush()
}

// currentURL returns the tailnet address, live from the server when it is
// running and otherwise the last one it had.
func currentURL(p paths) (string, error) {
	st, err := dial(p).status()
	if err == nil {
		switch {
		case st.Tailscale.URL != "":
			return st.Tailscale.URL, nil
		case st.Tailscale.State == "needs-login":
			return "", errors.New("not signed in to Tailscale; run `roi login`")
		case st.Tailscale.State == "off":
			return "", errors.New("this server was started with -no-tailnet")
		case st.Tailscale.Problem != "":
			return "", errors.New(st.Tailscale.Problem)
		}
		return "", errors.New("still connecting to Tailscale; try again in a moment")
	}
	if b, rerr := os.ReadFile(p.url); rerr == nil && len(b) > 0 {
		fmt.Fprintln(os.Stderr, "The server is not running; this is its last address. Start it with `roi start`.")
		return string(b), nil
	}
	return "", err
}

func cmdURL(args []string) error {
	if err := parse(newFlags("url", "Print the address to open on your phone."), args, 0); err != nil {
		return err
	}
	p, err := getPaths()
	if err != nil {
		return err
	}
	u, err := currentURL(p)
	if err != nil {
		return err
	}
	fmt.Println(u)
	return nil
}

func printQR(s string) {
	if q, err := qrcode.New(s, qrcode.Medium); err == nil {
		fmt.Print(q.ToSmallString(false))
	}
}

func cmdPair(args []string) error {
	if err := parse(newFlags("pair", "Show roi's address as a QR code. The phone must be signed in to the same tailnet."), args, 0); err != nil {
		return err
	}
	p, err := getPaths()
	if err != nil {
		return err
	}
	u, err := currentURL(p)
	if err != nil {
		return err
	}
	printQR(u)
	fmt.Printf("%s\n\nScan with your phone's camera. The phone needs the Tailscale app, connected to the same tailnet.\n", u)
	return nil
}

// ---- login, logout ----

// tailnet returns a handle on the Tailscale device: the running server's,
// or one started here when no server is running.
func tailnet(p paths) (tailnetCtl, func(), bool, error) {
	c := dial(p)
	if _, err := c.status(); err == nil {
		return c, func() {}, true, nil
	}
	cfg, _ := loadConfig(p.dir)
	n, err := openNode(p, cfg.Hostname, false, func(string, ...any) {})
	if err != nil {
		return nil, nil, false, fmt.Errorf("starting Tailscale: %v", err)
	}
	return n, func() { n.ts.Close() }, false, nil
}

// settle waits for the device to leave its starting state. A device with a
// saved identity can report needs-login for a moment while it loads, so that
// state only counts once it has held for two seconds.
func settle(ctx context.Context, t tailnetCtl, d time.Duration) (tsInfo, error) {
	var in tsInfo
	var err error
	var since time.Time
	waitFor(d, func() bool {
		in, err = t.Info(ctx)
		if err != nil || ctx.Err() != nil {
			return true
		}
		if in.State != "needs-login" {
			since = time.Time{}
			return in.State != "starting"
		}
		if since.IsZero() {
			since = time.Now()
		}
		return time.Since(since) > 2*time.Second
	})
	return in, err
}

func cmdLogin(args []string) error {
	fs := newFlags("login [--no-open] [--timeout 10m]", "Sign roi's Tailscale device in to your tailnet. Prints a sign-in link, opens it, and waits until you approve.")
	noOpen := fs.Bool("no-open", false, "do not open the link in the browser")
	timeout := fs.Duration("timeout", 10*time.Minute, "how long to wait for you to sign in")
	if err := parse(fs, args, 0); err != nil {
		return err
	}
	p, err := getPaths()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	t, done, running, err := tailnet(p)
	if err != nil {
		return err
	}
	defer done()

	in, err := settle(ctx, t, 15*time.Second)
	if err != nil {
		return err
	}
	if in.State == "off" {
		return errors.New("the running server was started with -no-tailnet")
	}
	signedIn := func(in tsInfo) {
		who := ""
		if in.User != "" {
			who = " as " + in.User
		}
		fmt.Printf("Signed in%s.\n", who)
		if in.URL != "" {
			os.WriteFile(p.url, []byte(in.URL), 0o600)
			fmt.Printf("roi's address is %s\n", in.URL)
		} else if in.Problem != "" {
			fmt.Printf("But roi cannot serve yet: %s\n", in.Problem)
		}
		if !running {
			fmt.Println("Start the server with: roi start")
		}
	}
	if in.State == "running" {
		who := ""
		if in.User != "" {
			who = " as " + in.User
		}
		fmt.Printf("Already signed in%s.\n", who)
		if in.URL == "" { // the address arrives a moment after the device comes up
			waitFor(8*time.Second, func() bool { in, err = t.Info(ctx); return err != nil || in.URL != "" || in.Problem != "" })
		}
		if in.URL != "" {
			os.WriteFile(p.url, []byte(in.URL), 0o600)
			fmt.Printf("roi's address is %s\n", in.URL)
		}
		return nil
	}
	if in.AuthURL == "" {
		if err := t.StartLogin(ctx); err != nil {
			return fmt.Errorf("asking Tailscale for a sign-in link: %v", err)
		}
		if !waitFor(30*time.Second, func() bool {
			in, err = t.Info(ctx)
			return err != nil || in.AuthURL != "" || in.State == "running" || ctx.Err() != nil
		}) || err != nil || (in.AuthURL == "" && in.State != "running") {
			return errors.New("Tailscale did not return a sign-in link; check this Mac's internet connection")
		}
	}
	if in.State != "running" {
		fmt.Print("Sign in to Tailscale to put roi on your tailnet:\n\n")
		printQR(in.AuthURL)
		fmt.Printf("  %s\n\n", in.AuthURL)
		if !*noOpen && runtime.GOOS == "darwin" {
			exec.Command("open", in.AuthURL).Start()
		}
		fmt.Println("Waiting for you to approve… (Ctrl+C to cancel)")
		told := false
		ok := waitFor(*timeout, func() bool {
			in, err = t.Info(ctx)
			if err == nil && in.State == "needs-approval" && !told {
				told = true
				fmt.Println("Signed in. Now waiting for an admin of your tailnet to approve the device…")
			}
			return err != nil || in.State == "running" || ctx.Err() != nil
		})
		if ctx.Err() != nil {
			return exitErr{130, "cancelled; the device is not signed in"}
		}
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("timed out waiting for the sign-in; run `roi login` again")
		}
	}
	// The address arrives a moment after the device comes up.
	waitFor(10*time.Second, func() bool {
		in, err = t.Info(ctx)
		return err != nil || in.URL != "" || in.Problem != ""
	})
	signedIn(in)
	return nil
}

func cmdLogout(args []string) error {
	fs := newFlags("logout [-y]", "Sign roi's Tailscale device out and forget its identity. Your phone cannot reach roi until you run `roi login` again.")
	yes := fs.Bool("y", false, "do not ask for confirmation")
	if err := parse(fs, args, 0); err != nil {
		return err
	}
	p, err := getPaths()
	if err != nil {
		return err
	}
	if !*yes {
		if !isTTY(os.Stdin) {
			return errors.New("refusing to sign out without confirmation; pass -y")
		}
		fmt.Print("Sign roi out of Tailscale? Your phone loses access until you run `roi login`. [y/N] ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			return exitErr{1, "not signed out"}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	t, done, running, err := tailnet(p)
	if err != nil {
		return err
	}
	in, _ := settle(ctx, t, 8*time.Second)
	var lerr error
	if in.State != "needs-login" {
		lerr = t.Logout(ctx)
	}
	done()
	os.Remove(p.url)
	if running {
		if lerr != nil {
			return fmt.Errorf("signing out: %v", lerr)
		}
		fmt.Println("Signed out. The server keeps running but is unreachable until you run `roi login`.")
		return nil
	}
	// No server holds the identity, so it can be deleted outright.
	if err := os.RemoveAll(p.tsnet); err != nil {
		return err
	}
	if lerr != nil {
		fmt.Printf("Tailscale could not be told (%v); the identity on this Mac is deleted anyway.\n", lerr)
		fmt.Println("Remove the device in the Tailscale admin console if it is still listed.")
		return nil
	}
	fmt.Println("Signed out and deleted the device's identity from this Mac.")
	return nil
}

// ---- devices, notify ----

func cmdDevices(args []string) error {
	fs := newFlags("devices [rm ID|NAME]", "List the phones subscribed to notifications, or remove one.")
	asJSON := fs.Bool("json", false, "print the list as JSON")
	if err := parse(fs, args, 2); err != nil {
		return err
	}
	p, err := getPaths()
	if err != nil {
		return err
	}
	c := dial(p)
	_, serr := c.status()
	var local *Pusher
	if serr != nil { // no server: work on the saved subscriptions directly
		if local, err = newPusher(p.dir, ""); err != nil {
			return err
		}
	}
	if fs.NArg() > 0 {
		if fs.Arg(0) != "rm" || fs.NArg() != 2 {
			return exitErr{2, "usage: roi devices [rm ID|NAME]"}
		}
		var r struct {
			Removed int `json:"removed"`
		}
		if local != nil {
			r.Removed = local.Remove(fs.Arg(1))
		} else if err := c.call(context.Background(), "POST", "/devices/remove", map[string]string{"key": fs.Arg(1)}, &r); err != nil {
			return err
		}
		if r.Removed == 0 {
			return fmt.Errorf("no subscribed device has the ID or name %q", fs.Arg(1))
		}
		fmt.Printf("Removed %d device%s. It no longer receives notifications.\n", r.Removed, map[bool]string{true: "s"}[r.Removed > 1])
		return nil
	}
	var r struct {
		Devices []Device `json:"devices"`
	}
	if local != nil {
		r.Devices = local.Devices()
	} else if err := c.call(context.Background(), "GET", "/devices", nil, &r); err != nil {
		return err
	}
	if *asJSON {
		b, _ := json.MarshalIndent(r.Devices, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	if len(r.Devices) == 0 {
		fmt.Println("No phone is subscribed. In roi on the phone, tap \"Mac online\", then \"Turn on notifications\".")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "ID\tDEVICE\tKIND\tADDED")
	for _, d := range r.Devices {
		added := "unknown"
		if !d.Added.IsZero() {
			added = d.Added.Local().Format("2006-01-02 15:04")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", d.ID, firstNonEmpty(d.Name, "unknown"), d.Kind, added)
	}
	return w.Flush()
}

func cmdNotify(args []string) error {
	fs := newFlags("notify [--title TITLE] TEXT... | -", "Send a notification to every subscribed phone. With - as the text, reads it from standard input.")
	title := fs.String("title", "roi", "the notification's title")
	if err := parse(fs, args, -1); err != nil {
		return err
	}
	text := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if text == "-" {
		b, _ := io.ReadAll(io.LimitReader(os.Stdin, 4096))
		text = strings.TrimSpace(string(b))
	}
	if text == "" {
		return exitErr{2, "nothing to send; usage: roi notify [--title TITLE] TEXT... | -"}
	}
	p, err := getPaths()
	if err != nil {
		return err
	}
	m := PushMsg{Title: *title, Body: clip(text, 1500), Kind: "note"}
	var r struct {
		Devices   int `json:"devices"`
		Delivered int `json:"delivered"`
	}
	c := dial(p)
	if _, serr := c.status(); serr == nil {
		if err := c.call(context.Background(), "POST", "/notify", m, &r); err != nil {
			return err
		}
	} else { // sending does not need the server
		cfg, _ := loadConfig(p.dir)
		push, err := newPusher(p.dir, cfg.PushContact)
		if err != nil {
			return err
		}
		r.Devices, r.Delivered = push.Count(), push.Send(m)
	}
	switch {
	case r.Devices == 0:
		return errors.New("no phone is subscribed; in roi on the phone, tap \"Mac online\", then \"Turn on notifications\"")
	case r.Delivered == 0:
		return errors.New("the push service did not accept the notification; see `roi logs`")
	}
	fmt.Printf("Sent to %d of %d device%s.\n", r.Delivered, r.Devices, map[bool]string{true: "s"}[r.Devices > 1])
	return nil
}
