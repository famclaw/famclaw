package gateway

import (
	"context"
	"log"
	"sync"
)

// SessionPool manages one goroutine per active user.
// Messages to the same user are processed serially (in order).
// Messages to different users are processed concurrently.
type SessionPool struct {
	mu       sync.Mutex
	sessions map[string]*userSession
	process  func(ctx context.Context, msg Message) Reply
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
func NewSessionPool(process func(ctx context.Context, msg Message) Reply) *SessionPool {
	return &SessionPool{
		sessions: make(map[string]*userSession),
		process:  process,
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
		// Queue full — drop oldest, enqueue new
		log.Printf("[session] %s queue full (10), dropping oldest message", userName)
		<-sess.queue
		sess.queue <- req
	}

	// Wait for reply or context cancellation
	select {
	case r := <-req.reply:
		return r
	case <-ctx.Done():
		return Reply{Text: "Request timed out.", PolicyAction: "error"}
	}
}

// runSession drains one user's queue sequentially.
func (p *SessionPool) runSession(userName string, sess *userSession) {
	for req := range sess.queue {
		reply := p.process(req.ctx, req.msg)
		select {
		case req.reply <- reply:
		default:
			// Caller already timed out
		}
	}
}
