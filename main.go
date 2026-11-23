// roi: a phone cockpit for the agents running in your Rex terminal panes.
//
// `roi serve` runs a web app on your tailnet through an embedded Tailscale
// node (tsnet): no system Tailscale, no VPN interface, nothing public. The
// other commands manage that server.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// version is set by the release build; a plain `go build` reports "dev".
var version = "dev"

const usageText = `roi — a phone cockpit for your Rex agents

Usage:
  roi COMMAND [options]

Server:
  serve       Run the server in the foreground
  start       Start the server in the background
  stop        Stop the server
  restart     Restart the server
  status      Show whether roi is running and reachable
  logs        Show the background server's log
  install     Start roi at login and keep it running (launchd)
  uninstall   Stop starting roi at login

Tailscale:
  login       Sign roi's device in to your tailnet
  logout      Sign the device out and forget its identity
  url         Print the address to open on your phone
  pair        Show that address as a QR code

Notifications:
  devices     List phones subscribed to notifications, or remove one
  notify      Send a notification to your phone

Other:
  doctor      Check everything roi depends on
  version     Print the version

Run "roi COMMAND -h" for a command's options.
`

// exitErr ends the program with a status; an empty message prints nothing.
type exitErr struct {
	code int
	msg  string
}

func (e exitErr) Error() string { return e.msg }

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usageText)
		return
	}
	cmds := map[string]func([]string) error{
		"serve": cmdServe, "start": cmdStart, "stop": cmdStop, "restart": cmdRestart,
		"status": cmdStatus, "logs": cmdLogs, "install": cmdInstall, "uninstall": cmdUninstall,
		"login": cmdLogin, "logout": cmdLogout, "url": cmdURL, "pair": cmdPair,
		"devices": cmdDevices, "notify": cmdNotify, "doctor": cmdDoctor,
		"version": func([]string) error { fmt.Println("roi", version); return nil },
		"help":    func([]string) error { fmt.Print(usageText); return nil },
	}
	name := os.Args[1]
	switch name {
	case "-h", "--help":
		name = "help"
	case "--version":
		name = "version"
	}
	run, ok := cmds[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "roi: unknown command %q\n\n", name)
		io.WriteString(os.Stderr, usageText)
		os.Exit(2)
	}
	if err := run(os.Args[2:]); err != nil {
		var e exitErr
		if errors.As(err, &e) {
			if e.msg != "" {
				fmt.Fprintln(os.Stderr, "roi:", e.msg)
			}
			os.Exit(e.code)
		}
		fmt.Fprintln(os.Stderr, "roi:", err)
		os.Exit(1)
	}
}
