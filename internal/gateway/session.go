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
	mu          sync.Mutex
	sessions    map[string]*userSession
	process     func(ctx context.Context, msg Message) Reply
	shutdownCtx context.Context
	shutdownFn  context.CancelFunc
}

type userSession struct {
	queue chan sessionRequest
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
	default:
		// Queue full — drop oldest with timeout, then enqueue new with timeout
		log.Printf("[session] %s queue full (10), dropping oldest message", userName)
		select {
		case <-sess.queue:
		case <-time.After(100 * time.Millisecond):
		}
		select {
		case sess.queue <- req:
		case <-time.After(100 * time.Millisecond):
			close(req.reply)
			return Reply{Text: "Request timed out.", PolicyAction: "error"}
		}
	}

	// Wait for reply or context cancellation
	select {
	case r := <-req.reply:
		return r
	case <-ctx.Done():
		return Reply{Text: "Request timed out.", PolicyAction: "error"}
	}
}

// Shutdown cancels the pool's shutdown context, signalling all
// session goroutines to exit and cancelling in-flight processing.
func (p *SessionPool) Shutdown() {
	p.shutdownFn()
}

// runSession drains one user's queue sequentially.
func (p *SessionPool) runSession(userName string, sess *userSession) {
	for {
		select {
		case <-p.shutdownCtx.Done():
			return
		case req, ok := <-sess.queue:
			if !ok {
				return
			}
			reply := p.process(p.shutdownCtx, req.msg)
			select {
			case req.reply <- reply:
			default:
				// Caller already timed out
			}
		}
	}
}
