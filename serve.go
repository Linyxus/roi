package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/tsnet"
)

// paths are the files roi keeps. ROI_HOME moves all of them, which is how
// a second, separate roi can run for testing.
type paths struct {
	dir   string // settings and state
	sock  string // control socket of the running server
	log   string // output of a background server
	tsnet string // the Tailscale device's identity
	url   string // the last URL the server was reachable at
}

func getPaths() (paths, error) {
	dir := os.Getenv("ROI_HOME")
	if dir == "" {
		cfg, err := os.UserConfigDir()
		if err != nil {
			return paths{}, err
		}
		dir = filepath.Join(cfg, "roi")
	}
	p := paths{dir: dir, sock: filepath.Join(dir, "roi.sock"), log: filepath.Join(dir, "roi.log"),
		tsnet: filepath.Join(dir, "tsnet"), url: filepath.Join(dir, "url")}
	return p, os.MkdirAll(p.tsnet, 0o700)
}

// ---- Tailscale ----

// tsInfo is the state of roi's Tailscale device.
type tsInfo struct {
	State     string     `json:"state"` // off | starting | needs-login | needs-approval | running
	AuthURL   string     `json:"authURL,omitempty"`
	URL       string     `json:"url,omitempty"`
	User      string     `json:"user,omitempty"`
	Hostname  string     `json:"hostname,omitempty"`
	KeyExpiry *time.Time `json:"keyExpiry,omitempty"`
	Problem   string     `json:"problem,omitempty"`
}

// tailnetCtl is how commands reach the Tailscale device: through the
// running server, or directly when no server is running.
type tailnetCtl interface {
	Info(ctx context.Context) (tsInfo, error)
	StartLogin(ctx context.Context) error
	Logout(ctx context.Context) error
}

type localNode struct {
	ts *tsnet.Server
	lc *local.Client
}

// openNode starts the Tailscale device from its saved identity. It does not
// wait for sign-in. userLogf receives the lines meant for a person.
func openNode(p paths, hostname string, verbose bool, userLogf func(string, ...any)) (*localNode, error) {
	ts := &tsnet.Server{Hostname: hostname, Dir: p.tsnet, UserLogf: userLogf}
	if !verbose {
		ts.Logf = func(string, ...any) {}
	}
	if err := ts.Start(); err != nil {
		return nil, err
	}
	lc, err := ts.LocalClient()
	if err != nil {
		ts.Close()
		return nil, err
	}
	return &localNode{ts, lc}, nil
}

func (n *localNode) Info(ctx context.Context) (tsInfo, error) {
	st, err := n.lc.StatusWithoutPeers(ctx)
	if err != nil {
		return tsInfo{}, err
	}
	in := tsInfo{State: "starting", AuthURL: st.AuthURL}
	switch st.BackendState {
	case "Running":
		in.State = "running"
	case "NeedsLogin":
		in.State = "needs-login"
	case "NeedsMachineAuth":
		in.State = "needs-approval"
	}
	if st.Self != nil {
		in.Hostname = st.Self.HostName
		if in.State == "running" {
			in.KeyExpiry = st.Self.KeyExpiry
			if u, ok := st.User[st.Self.UserID]; ok {
				in.User = u.LoginName
			} else if full, err := n.lc.Status(ctx); err == nil { // the short status can lack the owner's profile
				in.User = full.User[st.Self.UserID].LoginName
			}
		}
	}
	if in.State == "running" {
		in.AuthURL = ""
		switch {
		case len(st.CertDomains) > 0:
			in.URL = "https://" + st.CertDomains[0]
		case st.CurrentTailnet != nil && !st.CurrentTailnet.MagicDNSEnabled:
			in.Problem = "MagicDNS is off for this tailnet; turn it on in the Tailscale admin console"
		default:
			in.Problem = "HTTPS certificates are off for this tailnet; turn them on in the Tailscale admin console"
		}
	}
	return in, nil
}

func (n *localNode) StartLogin(ctx context.Context) error { return n.lc.StartLoginInteractive(ctx) }
func (n *localNode) Logout(ctx context.Context) error     { return n.lc.Logout(ctx) }

// ---- the server ----

// Status is what `roi status` shows about the running server.
type Status struct {
	Version    string         `json:"version"`
	PID        int            `json:"pid"`
	Started    time.Time      `json:"started"`
	Args       []string       `json:"args"`
	Service    bool           `json:"service"` // started by launchd
	Foreground bool           `json:"foreground"`
	Tailscale  tsInfo         `json:"tailscale"`
	Agents     map[string]int `json:"agents"`
	Devices    int            `json:"devices"`
	Dev        string         `json:"dev,omitempty"`
	Awake      string         `json:"awake"` // off | idle | holding
	Log        string         `json:"log"`
}

type daemon struct {
	p       paths
	started time.Time
	args    []string
	hub     *Hub
	node    *localNode // nil with -no-tailnet
	dev     string
	stop    context.CancelFunc

	mu      sync.Mutex
	problem string // why the tailnet listener is not serving
	awake   string
}

func serveFlags() (*flag.FlagSet, *serveOpts) {
	o := &serveOpts{}
	fs := flag.NewFlagSet("roi serve", flag.ContinueOnError)
	fs.StringVar(&o.hostname, "hostname", "", "tailnet device name (default: hostname in config.json, else roi)")
	fs.StringVar(&o.dev, "dev", "", "also serve without sign-in on this loopback address, e.g. 127.0.0.1:8787")
	fs.BoolVar(&o.noTailnet, "no-tailnet", false, "do not join the tailnet (use with -dev)")
	fs.StringVar(&o.rex, "rex", "", "path to the rex command (default: found on PATH)")
	fs.BoolVar(&o.awake, "awake", false, "keep the Mac from idle-sleeping while an agent is working")
	fs.BoolVar(&o.verbose, "verbose", false, "show Tailscale's own log lines")
	return fs, o
}

type serveOpts struct {
	hostname, dev, rex        string
	noTailnet, awake, verbose bool
}

func cmdServe(args []string) error {
	fs, o := serveFlags()
	if err := fs.Parse(args); err != nil {
		return exitErr{2, ""}
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("serve takes no arguments, got %q", fs.Arg(0))
	}
	if o.noTailnet && o.dev == "" {
		return errors.New("nothing to serve: -no-tailnet needs -dev")
	}
	p, err := getPaths()
	if err != nil {
		return err
	}
	if st, err := dial(p).status(); err == nil {
		return fmt.Errorf("already running (pid %d)", st.PID)
	}
	cfg, err := loadConfig(p.dir)
	if err != nil {
		log.Printf("%v; using defaults", err)
	}
	if o.hostname == "" {
		o.hostname = cfg.Hostname
	}
	if o.rex == "" {
		if o.rex, err = exec.LookPath("rex"); err != nil {
			return errors.New("rex was not found on PATH; pass -rex /path/to/rex")
		}
	}
	home, _ := os.UserHomeDir()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	os.Remove(p.sock) // left by a server that did not exit cleanly
	ctlLn, err := net.Listen("unix", p.sock)
	if err != nil {
		return err
	}
	defer os.Remove(p.sock)
	os.Chmod(p.sock, 0o600)

	rex := &Rex{bin: o.rex}
	hub := newHub(rex, home, p.dir)
	if hub.push, err = newPusher(p.dir, cfg.PushContact); err != nil {
		return err
	}
	machine, _ := os.Hostname()
	srv := &Server{hub: hub, rex: rex, cfg: cfg, host: strings.TrimSuffix(machine, ".local"), home: home}
	handler := srv.Handler()
	d := &daemon{p: p, started: time.Now(), args: args, hub: hub, dev: o.dev, stop: stop, awake: "off"}
	go hub.Run(ctx)

	errc := make(chan error, 3)
	if o.dev != "" {
		host, _, err := net.SplitHostPort(o.dev)
		if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
			return errors.New("-dev must be a loopback address such as 127.0.0.1:8787")
		}
		ln, err := net.Listen("tcp", o.dev)
		if err != nil {
			return err
		}
		log.Printf("serving without sign-in, this Mac only: http://%s", o.dev)
		go func() { errc <- http.Serve(ln, loopbackOnly(o.dev, handler)) }()
	}

	if !o.noTailnet {
		d.node, err = openNode(p, o.hostname, o.verbose, onceLogf())
		if err != nil {
			return fmt.Errorf("starting Tailscale: %v", err)
		}
		defer d.node.ts.Close()
		go func() {
			// Blocks until the device is signed in.
			ln, err := d.node.ts.ListenTLS("tcp", ":443")
			if err != nil {
				if ctx.Err() == nil {
					log.Printf("cannot serve on the tailnet: %v", err)
					d.mu.Lock()
					d.problem = strings.TrimPrefix(err.Error(), "tsnet: ")
					d.mu.Unlock()
				}
				return
			}
			errc <- http.Serve(ln, ownerOnly(d.node.lc, handler))
		}()
		go d.watchTailnet(ctx)
	}
	if o.awake {
		d.awake = "idle"
		go d.keepAwake(ctx)
	}
	go func() { errc <- http.Serve(ctlLn, d.control()) }()

	select {
	case <-ctx.Done():
		log.Print("stopping")
		return nil
	case err := <-errc:
		return err
	}
}

// onceLogf prints each distinct line once: tsnet repeats its sign-in link
// every few seconds while it waits.
func onceLogf() func(string, ...any) {
	var mu sync.Mutex
	last := ""
	return func(format string, a ...any) {
		s := fmt.Sprintf(format, a...)
		for _, noise := range []string{"tsnet running state path", "tsnet starting with hostname", "AuthLoop:"} {
			if strings.HasPrefix(s, noise) {
				return
			}
		}
		mu.Lock()
		defer mu.Unlock()
		if s != last {
			last = s
			log.Print(s)
		}
	}
}

func (d *daemon) info(ctx context.Context) tsInfo {
	if d.node == nil {
		return tsInfo{State: "off"}
	}
	in, err := d.node.Info(ctx)
	if err != nil {
		return tsInfo{State: "starting", Problem: err.Error()}
	}
	d.mu.Lock()
	if in.Problem == "" && in.State == "running" {
		in.Problem = d.problem
	}
	d.mu.Unlock()
	return in
}

// watchTailnet remembers the URL, and when the device loses its sign-in it
// asks Tailscale for a new link and sends that link to the subscribed phones.
func (d *daemon) watchTailnet(ctx context.Context) {
	prev, announced := "", ""
	for {
		in := d.info(ctx)
		if in.URL != "" && in.URL != announced {
			announced = in.URL
			os.WriteFile(d.p.url, []byte(in.URL), 0o600)
			log.Printf("roi is up: %s", in.URL)
		}
		if prev == "running" && in.State == "needs-login" {
			log.Print("Tailscale sign-in was lost; run `roi login`")
			d.node.StartLogin(ctx)
			link := ""
			for i := 0; i < 20 && link == "" && ctx.Err() == nil; i++ {
				time.Sleep(time.Second)
				link = d.info(ctx).AuthURL
			}
			d.hub.push.Send(PushMsg{Kind: "note", Title: "roi needs you to sign in",
				Body: "Its Tailscale sign-in ended, so your phone cannot reach it. Tap to sign in again.", URL: link})
		}
		if in.State != "starting" {
			prev = in.State
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(15 * time.Second):
		}
	}
}

// keepAwake holds a sleep assertion while any agent is working, and lets go
// two minutes after the last one stops.
func (d *daemon) keepAwake(ctx context.Context) {
	var caf *exec.Cmd
	release := func() {
		if caf != nil {
			caf.Process.Kill()
			caf.Wait()
			caf = nil
		}
	}
	defer release()
	var lastBusy time.Time
	for {
		busy := false
		for _, a := range d.hub.List() {
			if a.State == "working" {
				busy = true
			}
		}
		if busy {
			lastBusy = time.Now()
			if caf == nil {
				// -w ties the assertion to this process, so it cannot outlive roi.
				c := exec.Command("caffeinate", "-is", "-w", strconv.Itoa(os.Getpid()))
				if err := c.Start(); err == nil {
					caf = c
				} else {
					log.Printf("keep awake: %v", err)
				}
			}
		} else if caf != nil && time.Since(lastBusy) > 2*time.Minute {
			release()
		}
		d.mu.Lock()
		if caf != nil {
			d.awake = "holding"
		} else {
			d.awake = "idle"
		}
		d.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}

// control is the API behind the roi commands. It listens on a Unix socket
// that only the owner can open.
func (d *daemon) control() http.Handler {
	mux := http.NewServeMux()
	ok := func(w http.ResponseWriter) { writeJSON(w, map[string]bool{"ok": true}) }
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		st := Status{Version: version, PID: os.Getpid(), Started: d.started, Args: d.args,
			Service: os.Getenv("XPC_SERVICE_NAME") == launchdLabel, Foreground: isTTY(os.Stderr), Tailscale: d.info(r.Context()),
			Agents: map[string]int{"need": 0, "done": 0, "working": 0, "idle": 0}, Devices: d.hub.push.Count(),
			Dev: d.dev, Log: d.p.log}
		for _, a := range d.hub.List() {
			st.Agents[a.State]++
		}
		d.mu.Lock()
		st.Awake = d.awake
		d.mu.Unlock()
		writeJSON(w, st)
	})
	mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		if d.node == nil {
			fail(w, 409, errors.New("this server was started with -no-tailnet"))
			return
		}
		if err := d.node.StartLogin(r.Context()); err != nil {
			fail(w, 502, err)
			return
		}
		ok(w)
	})
	mux.HandleFunc("POST /logout", func(w http.ResponseWriter, r *http.Request) {
		if d.node == nil {
			fail(w, 409, errors.New("this server was started with -no-tailnet"))
			return
		}
		if err := d.node.Logout(r.Context()); err != nil {
			fail(w, 502, err)
			return
		}
		os.Remove(d.p.url)
		ok(w)
	})
	mux.HandleFunc("GET /devices", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"devices": d.hub.push.Devices()})
	})
	mux.HandleFunc("POST /devices/remove", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Key string `json:"key"`
		}
		if err := readBody(r, &b); err != nil || b.Key == "" {
			fail(w, 400, errors.New("no device given"))
			return
		}
		writeJSON(w, map[string]int{"removed": d.hub.push.Remove(b.Key)})
	})
	mux.HandleFunc("POST /notify", func(w http.ResponseWriter, r *http.Request) {
		var m PushMsg
		if err := readBody(r, &m); err != nil || strings.TrimSpace(m.Body) == "" {
			fail(w, 400, errors.New("no text given"))
			return
		}
		m.Kind, m.Agent = "note", ""
		devices := d.hub.push.Count()
		writeJSON(w, map[string]int{"devices": devices, "delivered": d.hub.push.Send(m)})
	})
	mux.HandleFunc("POST /stop", func(w http.ResponseWriter, r *http.Request) {
		ok(w)
		go func() { time.Sleep(100 * time.Millisecond); d.stop() }()
	})
	return mux
}

// ownerOnly admits only devices signed in as the same Tailscale user that
// owns this node. Reaching roi is the same as having a shell on the Mac.
func ownerOnly(lc *local.Client, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st, err := lc.StatusWithoutPeers(r.Context())
		who, werr := lc.WhoIs(r.Context(), r.RemoteAddr)
		if err != nil || werr != nil || st.Self == nil || who.UserProfile == nil ||
			who.Node == nil || who.Node.IsTagged() || who.UserProfile.ID != st.Self.UserID {
			http.Error(w, "roi only answers its owner's devices", http.StatusForbidden)
			return
		}
		r.Header.Set(deviceHeader, who.Node.ComputedName)
		next.ServeHTTP(w, r)
	})
}

// loopbackOnly refuses requests addressed to any other name, which blocks
// DNS-rebinding pages from reaching the unauthenticated listener.
func loopbackOnly(addr string, next http.Handler) http.Handler {
	_, port, _ := net.SplitHostPort(addr)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != addr && r.Host != "localhost:"+port {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		r.Header.Set(deviceHeader, "this Mac")
		next.ServeHTTP(w, r)
	})
}

// ---- the client side of the control socket ----

type ctl struct {
	p paths
	c *http.Client
}

func dial(p paths) *ctl {
	return &ctl{p, &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", p.sock)
		}}}}
}

func (c *ctl) call(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://roi"+path, body)
	if err != nil {
		return err
	}
	resp, err := c.c.Do(req)
	if err != nil {
		return errNotRunning
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&e)
		return errors.New(firstNonEmpty(e.Error, resp.Status))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

var errNotRunning = errors.New("the server is not running; start it with `roi start`")

func (c *ctl) status() (Status, error) {
	var st Status
	return st, c.call(context.Background(), "GET", "/status", nil, &st)
}

func (c *ctl) Info(ctx context.Context) (tsInfo, error) {
	var st Status
	err := c.call(ctx, "GET", "/status", nil, &st)
	return st.Tailscale, err
}
func (c *ctl) StartLogin(ctx context.Context) error { return c.call(ctx, "POST", "/login", nil, nil) }
func (c *ctl) Logout(ctx context.Context) error     { return c.call(ctx, "POST", "/logout", nil, nil) }
