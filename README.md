# roi

A phone cockpit for the Claude Code and Codex sessions running in your Rex
terminal panes. One Go binary on the Mac serves a web app on your tailnet
through an embedded Tailscale node; nothing is public and system Tailscale
stays off.

## Install

On a Mac with Apple silicon, with [Rex](https://www.superlogical.com/rex) installed:

```sh
curl -fsSL https://raw.githubusercontent.com/Linyxus/roi/main/install.sh | sh
```

This downloads the latest release, checks its checksum, and puts `roi` in
`~/.local/bin` (or `/usr/local/bin` if that is the one on your PATH). Set
`ROI_INSTALL_DIR` to choose another folder. Then:

```sh
roi login        # once: sign the device in to your tailnet
roi install      # start at login and keep running (launchd)
roi pair         # QR code of https://roi.<tailnet>.ts.net for the phone
```

Your tailnet needs MagicDNS and HTTPS certificates turned on, and the phone
needs the Tailscale app connected to the same tailnet.

To build from source instead: `go build -o roi .` (Go 1.27 or later).

Only devices signed in as the Tailscale user that owns the `roi` node are
admitted. Reaching roi is equivalent to a shell on the Mac.

## Commands

| Command | What it does |
| --- | --- |
| `roi serve [options]` | Run the server in the foreground |
| `roi start [options]` / `stop` / `restart` | Run it in the background; with the service installed, these drive the service |
| `roi install [options]` / `uninstall` | launchd agent `local.roi`: starts at login, restarts on crash |
| `roi status [--json]` | Running or not (exit 3), address, sign-in, key expiry, agent counts |
| `roi logs [-f] [-n N]` | The background server's log |
| `roi login` / `logout` | Sign the Tailscale device in or out |
| `roi url` / `pair` | Print the address, or show it as a QR code |
| `roi devices [rm ID]` | Phones subscribed to notifications |
| `roi notify [--title T] TEXT` | Send a notification to those phones (`-` reads stdin) |
| `roi doctor` | Check Rex, the server, Tailscale, launch commands, sleep, notifications |

Options for `serve`, `start` and `install`:

- `-awake` keeps the Mac from idle-sleeping while an agent is working. It
  does not survive closing the lid.
- `-dev 127.0.0.1:8787` also serves on this Mac without sign-in, for testing.
- `-no-tailnet` skips Tailscale (needs `-dev`).
- `-hostname NAME` sets the tailnet device name; `-rex PATH` the rex command.

`start` and `install` refuse to run until the device is signed in, so a
background server never waits on a sign-in link nobody sees. If the sign-in
is lost while the server runs, it pushes a notification with a new link.

The commands talk to the running server over `roi.sock` in the state
directory, which only the owner can open. `ROI_HOME` moves the state
directory, which gives a second, separate roi for testing.

## What it does

- **Inbox**: every agent pane, sorted needs-you → finished → working → idle,
  pushed live from Rex's program-status events.
- **Session**: the conversation read from the agent's own session file, with
  tool calls folded into steps, diffs, the plan and sub-agents.
- **Prompts**: a blocked agent's choices are read off its screen and shown as
  buttons; tapping one presses that number. Typing instead presses Esc and
  sends your text.
- **Terminal**: the raw pane, with a key bar, for anything else.
- **New agent**: start Claude Code or Codex in a folder, with a first prompt.
- **Notifications**: Web Push when an agent needs you, and 30 seconds after
  one finishes. On iPhone this needs roi added to the Home Screen.

## Settings

`~/Library/Application Support/roi/config.json`:

```json
{
  "launch": {
    "claude": ["claude", "--permission-mode", "acceptEdits"],
    "codex": ["fish", "-c", "my-codex $argv"]
  }
}
```

`hostname` (default `roi`) names the tailnet device. `launch` is the command that starts each kind of agent; the first prompt is
appended as one argument. Rex `exec`s the command, so a shell function or
alias has to be wrapped in a shell, as the second line does. State (Tailscale identity, push
keys, read markers) lives in the same directory.

## Layout

| File | Role |
| --- | --- |
| `main.go` | command dispatch |
| `serve.go` | the server: Tailscale listener, access control, control socket |
| `cli.go` | status, login, pair, devices, notify |
| `service.go` | start, stop, install (launchd), logs |
| `doctor.go` | `roi doctor` |
| `rex.go` | calls to the Rex server through the `rex` CLI |
| `hub.go` | agent list, status tracking, prompt parsing |
| `transcript.go` | Claude Code and Codex session-file readers |
| `api.go` | HTTP API and spawning |
| `push.go` | Web Push |
| `web/` | the app: plain HTML, CSS and JS, embedded in the binary |
| `install.sh` | the one-line installer |
| `release.sh` | tags a release and uploads the binary |
| `test/shot.mjs` | drives the app in headless Chrome at phone size |

## Test

```sh
go test ./...
ROI_HOME=/tmp/roi-test ./roi start -no-tailnet -dev 127.0.0.1:8787
node test/shot.mjs /tmp/roi-shots inbox= new=/new settings=/settings
```

## Release

```sh
./release.sh v0.3.0
```

Runs the tests, builds `roi` for macOS on Apple silicon with the version
stamped in, tags the commit, pushes the tag, and publishes a GitHub release
with `roi-darwin-arm64.tar.gz` and its checksum. It needs a clean working
tree on `main` and the `gh` CLI signed in.
