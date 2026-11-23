package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Rex talks to the local Rex server through the rex CLI.
type Rex struct{ bin string }

func (r *Rex) run(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.bin, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("rex %s: %s", args[0], msg)
	}
	return out.Bytes(), nil
}

// api calls a server method. session may be empty.
func (r *Rex) api(session, method string, args any, out any) error {
	a := []string{"api", "call"}
	if session != "" {
		a = append(a, "-s", session)
	}
	a = append(a, method)
	if args != nil {
		b, err := json.Marshal(args)
		if err != nil {
			return err
		}
		a = append(a, string(b))
	}
	b, err := r.run(a...)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(b, out)
}

// term calls a terminal block method.
func (r *Rex) term(session, block, method string, args any, out any) error {
	if args == nil {
		args = map[string]any{}
	}
	return r.api(session, "com.superlogical.terminal."+method, map[string]any{"block_id": block, "args": args}, out)
}

type rexSession struct {
	ID    string `json:"session_id"`
	Label string `json:"label"`
}

func (r *Rex) sessions() ([]rexSession, error) {
	var v struct {
		Sessions []rexSession `json:"sessions"`
	}
	err := r.api("", "session.list", nil, &v)
	return v.Sessions, err
}

type rexBlock struct {
	ID, Window, WindowLabel, Creator string
}

func (r *Rex) blocks(session string) ([]rexBlock, error) {
	var v struct {
		Windows []struct {
			ID     string `json:"window_id"`
			Label  string `json:"label"`
			Layers []struct {
				Blocks []struct {
					ID      string `json:"block_id"`
					Creator string `json:"creator_name"`
				} `json:"blocks"`
			} `json:"layers"`
		} `json:"windows"`
	}
	if err := r.api(session, "session.view", nil, &v); err != nil {
		return nil, err
	}
	var out []rexBlock
	for _, w := range v.Windows {
		for _, l := range w.Layers {
			for _, b := range l.Blocks {
				out = append(out, rexBlock{b.ID, w.ID, w.Label, b.Creator})
			}
		}
	}
	return out, nil
}

type procInfo struct {
	PID   int    `json:"pid"`
	Name  string `json:"name"`
	Path  string `json:"path"`
	Argv0 string `json:"argv0"`
	Cwd   string `json:"cwd"`
}

type statusRecord struct {
	ID        string `json:"id"`
	State     string `json:"state"`
	Kind      string `json:"kind"`
	App       string `json:"effective_app"`
	Msg       string `json:"msg"`
	Title     string `json:"title"`
	Progress  *int   `json:"progress"`
	UpdatedAt string `json:"updated_at"`
	Owner     *struct {
		PID int `json:"pid"`
	} `json:"owner"`
}

func (r *Rex) process(s, b string) (procInfo, error) {
	var v struct {
		Foreground procInfo `json:"foreground"`
	}
	err := r.term(s, b, "process", nil, &v)
	return v.Foreground, err
}

// exited reports whether the pane's program has already ended.
func (r *Rex) exited(s, b string) bool {
	var v struct {
		LastExit *struct{} `json:"last_exit"`
	}
	if err := r.term(s, b, "process", nil, &v); err != nil {
		return true
	}
	return v.LastExit != nil
}

func (r *Rex) status(s, b string) ([]statusRecord, error) {
	var v struct {
		Records []statusRecord `json:"records"`
	}
	err := r.term(s, b, "program_status", nil, &v)
	return v.Records, err
}

func (r *Rex) title(s, b string) string {
	var v struct {
		Title string `json:"title"`
	}
	r.term(s, b, "title", nil, &v)
	return v.Title
}

// screenText returns the last lines of the terminal as plain text.
func (r *Rex) screenText(s, b string, lines int) (string, error) {
	var v struct {
		Content string `json:"content"`
	}
	if err := r.term(s, b, "format", map[string]any{"format": "text", "trim": true}, &v); err != nil {
		return "", err
	}
	return lastLines(strings.TrimRight(v.Content, "\n "), lines), nil
}

// screenHTML returns the tail of the terminal as Rex's HTML rendering.
func (r *Rex) screenHTML(s, b string) (string, error) {
	const window = 160 << 10
	var v struct {
		Content string `json:"content"`
		ID      string `json:"id"`
		Total   int    `json:"total"`
	}
	if err := r.term(s, b, "format", map[string]any{"format": "html", "trim": true, "max_bytes": window}, &v); err != nil {
		return "", err
	}
	if v.ID == "" || v.Total <= len(v.Content) {
		return v.Content, nil
	}
	var rest struct {
		Content string `json:"content"`
	}
	off := v.Total - window
	if off < 0 {
		off = 0
	}
	if err := r.term(s, b, "blob_read", map[string]any{"id": v.ID, "offset": off}, &rest); err != nil {
		return v.Content, nil
	}
	if i := strings.IndexByte(rest.Content, '\n'); i >= 0 && off > 0 {
		return rest.Content[i+1:], nil
	}
	return rest.Content, nil
}

type termSize struct {
	Columns int `json:"columns"`
	Rows    int `json:"rows"`
}

func (r *Rex) size(s, b string) termSize {
	var v termSize
	r.term(s, b, "size", nil, &v)
	return v
}

// paste types text into the terminal as a paste, so newlines do not submit.
func (r *Rex) paste(s, b, text string) error {
	return r.api(s, "block.event", map[string]any{
		"block_id": b,
		"event":    map[string]any{"type": "paste", "payload": map[string]any{"text": text}},
	}, nil)
}

var keyRe = regexp.MustCompile(`^(?:(?:ctrl|shift|alt|cmd)\+)*(?:[a-z0-9]|enter|escape|tab|space|backspace|delete|up|down|left|right|home|end|pageup|pagedown)$`)

func (r *Rex) keys(s, b string, keys ...string) error {
	for _, k := range keys {
		if !keyRe.MatchString(k) {
			return fmt.Errorf("key %q is not allowed", k)
		}
	}
	_, err := r.run(append([]string{"send-key", "-s", s, "-b", b}, keys...)...)
	return err
}

// events streams server events until the command exits, calling fn for each.
func (r *Rex) events(ctx context.Context, fn func(session, block, name string)) error {
	cmd := exec.CommandContext(ctx, r.bin, "events", "--all", "--json")
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var e struct {
			Type    string `json:"type"`
			Payload struct {
				SessionID string `json:"session_id"`
				Event     struct {
					Type    string `json:"type"`
					Payload struct {
						BlockID string `json:"block_id"`
						Name    string `json:"name"`
					} `json:"payload"`
				} `json:"event"`
			} `json:"payload"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		name := e.Type
		if e.Payload.Event.Type != "" {
			name = e.Payload.Event.Type
		}
		if e.Payload.Event.Payload.Name != "" {
			name = e.Payload.Event.Payload.Name
		}
		fn(e.Payload.SessionID, e.Payload.Event.Payload.BlockID, name)
	}
	return cmd.Wait()
}

func lastLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
