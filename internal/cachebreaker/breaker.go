// Package cachebreaker — circuit breaker для Redis-клиента в виде хука go-redis.
package cachebreaker

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/sony/gobreaker/v2"
)

// Hook пропускает команды Redis через breaker: после failures сбоев подряд
// команды сразу возвращают gobreaker.ErrOpenState, через timeout идёт пробный вызов.
type Hook struct {
	cb *gobreaker.CircuitBreaker[any]
}

// New создаёт Hook; redis.Nil (промах) сбоем не считается.
func New(failures uint32, timeout time.Duration) *Hook {
	return &Hook{cb: gobreaker.NewCircuitBreaker[any](gobreaker.Settings{
		Name:         "redis",
		MaxRequests:  1,
		Timeout:      timeout,
		ReadyToTrip:  func(c gobreaker.Counts) bool { return c.ConsecutiveFailures >= failures },
		IsSuccessful: func(err error) bool { return err == nil || errors.Is(err, redis.Nil) },
	})}
}

// DialHook не меняет установку соединения.
func (h *Hook) DialHook(next redis.DialHook) redis.DialHook { return next }

// ProcessHook выполняет команду через breaker.
func (h *Hook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		_, err := h.cb.Execute(func() (any, error) { return nil, next(ctx, cmd) })
		if err != nil {
			cmd.SetErr(err)
		}
		return err
	}
}

// ProcessPipelineHook выполняет конвейер через breaker.
func (h *Hook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		_, err := h.cb.Execute(func() (any, error) { return nil, next(ctx, cmds) })
		return err
	}
}

var _ redis.Hook = (*Hook)(nil)
