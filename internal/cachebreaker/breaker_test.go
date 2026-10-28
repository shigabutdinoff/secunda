package cachebreaker

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/sony/gobreaker/v2"
)

func TestHook(t *testing.T) {
	calls := 0
	var result error
	next := func(context.Context, redis.Cmder) error { calls++; return result }
	run := New(3, 50*time.Millisecond).ProcessHook(next)
	ctx := context.Background()
	cmd := func() redis.Cmder { return redis.NewStringCmd(ctx, "get", "k") }

	result = redis.Nil
	for range 5 {
		_ = run(ctx, cmd())
	}
	result = errors.New("dial tcp: refused")
	for range 3 {
		_ = run(ctx, cmd())
	}
	if calls != 8 {
		t.Fatalf("redis.Nil открыл breaker: calls=%d", calls)
	}
	c := cmd()
	if err := run(ctx, c); !errors.Is(err, gobreaker.ErrOpenState) || !errors.Is(c.Err(), gobreaker.ErrOpenState) || calls != 8 {
		t.Fatalf("open: err=%v calls=%d", err, calls)
	}

	time.Sleep(60 * time.Millisecond)
	result = nil
	if err := run(ctx, cmd()); err != nil || calls != 9 {
		t.Fatalf("half-open probe: err=%v calls=%d", err, calls)
	}
	if err := run(ctx, cmd()); err != nil || calls != 10 {
		t.Fatalf("closed: err=%v calls=%d", err, calls)
	}
}

func TestPipelineAndDialHooks(t *testing.T) {
	h := New(1, time.Minute)
	dial := func(context.Context, string, string) (net.Conn, error) { return nil, nil }
	if h.DialHook(dial) == nil {
		t.Fatal("DialHook вернул nil")
	}
	calls := 0
	run := h.ProcessPipelineHook(func(context.Context, []redis.Cmder) error { calls++; return errors.New("refused") })
	ctx := context.Background()
	_ = run(ctx, nil)
	if err := run(ctx, nil); !errors.Is(err, gobreaker.ErrOpenState) || calls != 1 {
		t.Fatalf("pipeline: err=%v calls=%d", err, calls)
	}
}
