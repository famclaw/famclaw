package gateway

import (
	"context"
	"log"
	"sync"
	"time"
)

// SessionPool manages one goroutine per active user.
// Messages to the same user are processed serially (in order).
// Messages to different users are processed concurrently.
type SessionPool struct {
	mu           sync.Mutex
	sessions     map[string]*userSession
	process      func(ctx context.Context, msg Message) Reply
	shutdownCtx  context.Context
	shutdownFn   context.CancelFunc
	shutdownOnce sync.Once
}

type userSession struct {
	queue chan sessionRequest
	// done is closed by Shutdown to signal that no new messages should be
	// accepted. Dispatch selects on done to avoid sending after shutdown.
	// The queue channel is intentionally NOT closed (closing a channel that
	// has concurrent senders is a data race); done serves as the shutdown
	// signal instead.
	done chan struct{}
}

type sessionRequest struct {
	ctx   context.Context
	msg   Message
	reply chan Reply
}

// NewSessionPool creates a pool that dispatches to per-user goroutines.
// The process function is called sequentially per user.
// The shutdownCtx is used to cancel all in-flight processing and
// exit session goroutines when the pool is shut down.
func NewSessionPool(shutdownCtx context.Context, process func(ctx context.Context, msg Message) Reply) *SessionPool {
	ctx, cancel := context.WithCancel(shutdownCtx)
	return &SessionPool{
		sessions:    make(map[string]*userSession),
		process:     process,
		shutdownCtx: ctx,
		shutdownFn:  cancel,
	}
}

// Dispatch sends a message to the user's session goroutine and waits for the reply.
// Returns immediately if the user's queue is full (drops oldest).
func (p *SessionPool) Dispatch(ctx context.Context, userName string, msg Message) Reply {
	p.mu.Lock()
	sess, ok := p.sessions[userName]
	if !ok {
		sess = &userSession{
			queue: make(chan sessionRequest, 10),
			done:  make(chan struct{}),
		}
		p.sessions[userName] = sess
		go p.runSession(userName, sess)
	}
	p.mu.Unlock()

	req := sessionRequest{
		ctx:   ctx,
		msg:   msg,
		reply: make(chan Reply, 1),
	}

	select {
	case sess.queue <- req:
		// Queued — wait for reply
	case <-sess.done:
		// Session is shutting down
		return Reply{Text: "Request timed out.", PolicyAction: "error"}
	default:
		// Queue full — drop oldest with timeout, then enqueue new with timeout
		log.Printf("[session] %s queue full (10), dropping oldest message", userName)
		select {
		case dropped, ok := <-sess.queue:
			if ok && dropped.reply != nil {
				select {
				case dropped.reply <- Reply{Text: "Request timed out.", PolicyAction: "error"}:
				default:
				}
			}
		case <-sess.done:
			return Reply{Text: "Request timed out.", PolicyAction: "error"}
		case <-time.After(100 * time.Millisecond):
		}
		select {
		case sess.queue <- req:
		case <-sess.done:
			return Reply{Text: "Request timed out.", PolicyAction: "error"}
		case <-time.After(100 * time.Millisecond):
			close(req.reply)
			return Reply{Text: "Request timed out.", PolicyAction: "error"}
		}
	}

	// Wait for reply or context cancellation or session shutdown
	select {
	case r := <-req.reply:
		return r
	case <-ctx.Done():
		return Reply{Text: "Request timed out.", PolicyAction: "error"}
	case <-sess.done:
		return Reply{Text: "Request timed out.", PolicyAction: "error"}
	}
}

// Shutdown signals all session goroutines to stop accepting new work.
// Closing done (not the queue) avoids the data race between closing a
// channel and concurrent sends. In-flight and already-queued messages
// continue to be processed by the drain loop until done is observed.
// Shutdown is idempotent.
func (p *SessionPool) Shutdown() {
	p.shutdownOnce.Do(func() {
		p.shutdownFn()
		p.mu.Lock()
		sessions := make([]*userSession, 0, len(p.sessions))
		for _, sess := range p.sessions {
			sessions = append(sessions, sess)
		}
		p.mu.Unlock()
		for _, sess := range sessions {
			close(sess.done)
		}
	})
}

// runSession drains one user's queue sequentially.
// When the shutdown context is cancelled, instead of exiting immediately,
// it keeps draining and processing queued requests. It only exits when
// done is closed (by Shutdown), at which point it performs a final
// non-blocking scan of any messages that were queued before done was
// closed, ensuring in-flight and already-queued messages still receive
// their replies.
func (p *SessionPool) runSession(userName string, sess *userSession) {
	for {
		select {
		case req, ok := <-sess.queue:
			if !ok {
				return
			}
			p.handleRequest(req)
		case <-p.shutdownCtx.Done():
			// Shutdown signalled: drain remaining queued requests to
			// completion rather than discarding them. The inner loop
			// processes messages until done is closed, then performs
			// a final non-blocking drain.
			for {
				select {
				case req, ok := <-sess.queue:
					if !ok {
						return
					}
					p.handleRequest(req)
				case <-sess.done:
					// done is closed — no more messages will be sent
					// (Dispatch checks done before sending). Perform a
					// final non-blocking scan of any messages that were
					// queued before done was closed.
					for {
						select {
						case req, ok := <-sess.queue:
							if !ok {
								return
							}
							p.handleRequest(req)
						default:
							return
						}
					}
				}
			}
		}
	}
}

// handleRequest processes a single request and delivers the reply.
// The per-request context is derived from the caller's context and
// cancelled automatically when the handler returns, ensuring no
// resource leaks. The caller's context (not the shutdown context)
// governs cancellation of in-flight processing, so messages that are
// already being processed complete normally.
func (p *SessionPool) handleRequest(req sessionRequest) {
	ctx, cancel := context.WithCancel(req.ctx)
	defer cancel()
	reply := p.process(ctx, req.msg)
	select {
	case req.reply <- reply:
	default:
		// Caller already timed out
	}
}
