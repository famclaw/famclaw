// Package browser drives a remote Playwright server (e.g. the mcr.microsoft.com/playwright
// container) to give famclaw real browser navigation: navigate, click, fill, extract.
//
// Per-user singleton page: one tab per famclaw user, idle-closed after IdleTimeout.
// Host allowlist is enforced at the call site (see HostValidator on Exec).
package browser

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/playwright-community/playwright-go"
)

// Config controls a Pool.
type Config struct {
	Endpoint    string
	IdleTimeout time.Duration
}

// Pool owns a single connection to the Playwright server and per-user pages.
type Pool struct {
	cfg     Config
	pw      *playwright.Playwright
	browser playwright.Browser

	// ctx + cancel scope the idleSweeper goroutine. Cancelling ctx (or
	// calling Close) stops the sweeper. playwright-go does not currently
	// expose context-aware Run/Connect, so the initial blocking init in
	// NewPool is not yet ctx-aware — see the note there.
	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	sessions map[string]*userSession
	closed   bool
}

type userSession struct {
	bctx     playwright.BrowserContext
	page     playwright.Page
	refs     map[string]RefEntry
	prevRefs map[string]string // refKey → ref id; survives across snapshots, reset on navigate
	lastUsed time.Time
}

// NewPool boots playwright-go and connects to cfg.Endpoint. ctx scopes
// the per-pool idleSweeper goroutine; cancelling ctx (or calling Close)
// stops the sweeper. The blocking playwright.Run() and Chromium.Connect()
// are NOT yet ctx-aware — playwright-go v0.5700.x does not expose
// context-aware initialization. ctx is accepted now so callers can pass
// a cancellable scope and the sweeper picks it up immediately; future
// playwright-go versions can wire ctx into Run/Connect without an API
// change here.
func NewPool(ctx context.Context, cfg Config) (*Pool, error) {
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return nil, fmt.Errorf("browser: endpoint not configured")
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = 5 * time.Minute
	}
	pw, err := playwright.Run()
	if err != nil {
		return nil, fmt.Errorf("browser: playwright.Run: %w", err)
	}
	br, err := pw.Chromium.Connect(cfg.Endpoint)
	if err != nil {
		_ = pw.Stop()
		return nil, fmt.Errorf("browser: connect %s: %w", cfg.Endpoint, err)
	}
	pctx, cancel := context.WithCancel(ctx)
	p := &Pool{
		cfg:      cfg,
		pw:       pw,
		browser:  br,
		ctx:      pctx,
		cancel:   cancel,
		sessions: make(map[string]*userSession),
	}
	go p.idleSweeper()
	return p, nil
}

// Close closes per-user contexts, then the browser and the playwright driver.
func (p *Pool) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	sessions := p.sessions
	p.sessions = nil
	if p.cancel != nil {
		p.cancel()
	}
	p.mu.Unlock()

	for name, s := range sessions {
		if err := s.bctx.Close(); err != nil {
			log.Printf("[browser] close ctx for %q: %v", name, err)
		}
	}
	if err := p.browser.Close(); err != nil {
		log.Printf("[browser] close browser: %v", err)
	}
	return p.pw.Stop()
}

func (p *Pool) dropSession(user string) {
	p.mu.Lock()
	s, ok := p.sessions[user]
	if ok {
		delete(p.sessions, user)
	}
	p.mu.Unlock()
	if ok {
		_ = s.bctx.Close()
	}
}

func (p *Pool) idleSweeper() {
	period := p.cfg.IdleTimeout / 4
	if period < 30*time.Second {
		period = 30 * time.Second
	}
	t := time.NewTicker(period)
	defer t.Stop()
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-t.C:
		}
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return
		}
		now := time.Now()
		var stale []string
		for user, s := range p.sessions {
			if now.Sub(s.lastUsed) >= p.cfg.IdleTimeout {
				stale = append(stale, user)
			}
		}
		p.mu.Unlock()
		for _, user := range stale {
			log.Printf("[browser] idle-closing session for %q", user)
			p.dropSession(user)
		}
	}
}
