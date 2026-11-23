package main

import (
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
)

// DeviceMeta is what roi remembers about a browser that subscribed.
type DeviceMeta struct {
	Name  string    `json:"name"` // the device's Tailscale name
	UA    string    `json:"ua"`
	Added time.Time `json:"added"`
}

// Device is one subscribed browser, as listed by `roi devices`.
type Device struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Kind    string    `json:"kind"`
	Service string    `json:"service"`
	Added   time.Time `json:"added"`
}

// Pusher sends Web Push notifications to the browsers that subscribed.
type Pusher struct {
	mu      sync.Mutex
	path    string
	contact string
	Public  string                          `json:"public"`
	Private string                          `json:"private"`
	Subs    map[string]webpush.Subscription `json:"subs"` // by endpoint
	Meta    map[string]DeviceMeta           `json:"meta"` // by endpoint
}

func newPusher(dir, contact string) (*Pusher, error) {
	p := &Pusher{path: filepath.Join(dir, "push.json"), contact: contact}
	if b, err := os.ReadFile(p.path); err == nil {
		json.Unmarshal(b, p)
	}
	if p.Private == "" {
		priv, pub, err := webpush.GenerateVAPIDKeys()
		if err != nil {
			return nil, err
		}
		p.Private, p.Public = priv, pub
		p.save()
	}
	if p.Subs == nil {
		p.Subs = map[string]webpush.Subscription{}
	}
	if p.Meta == nil {
		p.Meta = map[string]DeviceMeta{}
	}
	return p, nil
}

func (p *Pusher) save() {
	b, _ := json.MarshalIndent(p, "", "  ")
	os.WriteFile(p.path, b, 0o600)
}

func (p *Pusher) Add(s webpush.Subscription, m DeviceMeta) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if old, ok := p.Meta[s.Endpoint]; ok && !old.Added.IsZero() {
		m.Added = old.Added
	}
	p.Subs[s.Endpoint] = s
	p.Meta[s.Endpoint] = m
	p.save()
}

func (p *Pusher) Count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.Subs)
}

func deviceKind(ua string) string {
	for _, k := range []string{"iPhone", "iPad", "Android"} {
		if strings.Contains(ua, k) {
			return k
		}
	}
	if strings.Contains(ua, "Macintosh") {
		return "Mac"
	}
	if ua == "" {
		return "unknown"
	}
	return "browser"
}

// Devices lists the subscribed browsers, oldest first.
func (p *Pusher) Devices() []Device {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Device, 0, len(p.Subs))
	for ep := range p.Subs {
		m := p.Meta[ep]
		d := Device{ID: fnv(ep)[:8], Name: m.Name, Kind: deviceKind(m.UA), Added: m.Added}
		if u, err := url.Parse(ep); err == nil {
			d.Service = u.Host
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Added.Equal(out[j].Added) {
			return out[i].Added.Before(out[j].Added)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Remove forgets the devices whose ID or name is key and reports how many.
func (p *Pusher) Remove(key string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for ep := range p.Subs {
		if fnv(ep)[:8] == key || (key != "" && strings.EqualFold(p.Meta[ep].Name, key)) {
			delete(p.Subs, ep)
			delete(p.Meta, ep)
			n++
		}
	}
	if n > 0 {
		p.save()
	}
	return n
}

// PushMsg is the payload the service worker turns into a notification.
type PushMsg struct {
	Agent string `json:"agent,omitempty"`
	Title string `json:"title"`
	Body  string `json:"body"`
	Kind  string `json:"kind"` // need | done | note
	URL   string `json:"url,omitempty"`
}

// Notify sends an agent notification to every subscription.
func (p *Pusher) Notify(agent, title, body, kind string) int {
	return p.Send(PushMsg{Agent: agent, Title: title, Body: body, Kind: kind})
}

// Send delivers one notification to every subscription and reports how many
// push services accepted it.
func (p *Pusher) Send(m PushMsg) int {
	p.mu.Lock()
	subs := make([]webpush.Subscription, 0, len(p.Subs))
	for _, s := range p.Subs {
		subs = append(subs, s)
	}
	p.mu.Unlock()
	msg, _ := json.Marshal(m)
	urgency := webpush.UrgencyNormal
	if m.Kind == "need" {
		urgency = webpush.UrgencyHigh
	}
	opts := &webpush.Options{
		Subscriber: p.contact, VAPIDPublicKey: p.Public, VAPIDPrivateKey: p.Private,
		TTL: 3600, Urgency: urgency,
	}
	if m.Kind != "note" { // a newer agent notification replaces an undelivered older one
		opts.Topic = "roi-" + m.Kind
	}
	ok := 0
	for _, s := range subs {
		s := s
		resp, err := webpush.SendNotification(msg, &s, opts)
		if err != nil {
			log.Printf("push: %v", err)
			continue
		}
		resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusGone || resp.StatusCode == http.StatusNotFound:
			p.mu.Lock()
			delete(p.Subs, s.Endpoint)
			delete(p.Meta, s.Endpoint)
			p.save()
			p.mu.Unlock()
		case resp.StatusCode >= 300:
			log.Printf("push: service answered %s", resp.Status)
		default:
			ok++
		}
	}
	return ok
}
