package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestShutdownWebConcurrentSharedDeadline(t *testing.T) {
	t.Run("background completes while http waits", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		entered := make(chan struct{})
		release := make(chan struct{})
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = shutdownWeb(ctx, func(ctx context.Context) error {
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}, func(context.Context) error { close(entered); return nil })
		}()
		select {
		case <-entered:
		case <-time.After(500 * time.Millisecond):
			close(release)
			<-done
			t.Fatal("background drain did not start while HTTP was blocked")
		}
		close(release)
		select {
		case <-done:
		case <-ctx.Done():
			t.Fatal("shutdown did not complete")
		}
	})
	t.Run("both wait on same deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		wait := func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
		start := time.Now()
		httpErr, bgErr := shutdownWeb(ctx, wait, wait)
		if !errors.Is(httpErr, context.DeadlineExceeded) || !errors.Is(bgErr, context.DeadlineExceeded) {
			t.Fatalf("errors http=%v background=%v", httpErr, bgErr)
		}
		if time.Since(start) > time.Second {
			t.Fatal("shutdown exceeded shared deadline")
		}
	})
}
