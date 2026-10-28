package httpapi

import (
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// RateLimiter ограничивает частоту запросов по пользователю или, для анонимных, по IP.
// Ключи, к которым не обращались limiterIdle, удаляются при обходе раз в limiterIdle.
type RateLimiter struct {
	mu        sync.Mutex
	limiters  map[string]*limiterEntry
	rps       rate.Limit
	burst     int
	now       func() time.Time
	lastSweep time.Time
}

type limiterEntry struct {
	lim  *rate.Limiter
	seen time.Time
}

const limiterIdle = 10 * time.Minute

// NewRateLimiter создаёт ограничитель: rps запросов в секунду с запасом burst на ключ.
func NewRateLimiter(rps float64, burst int) *RateLimiter {
	return &RateLimiter{limiters: map[string]*limiterEntry{}, rps: rate.Limit(rps), burst: burst, now: time.Now}
}

func (l *RateLimiter) limiter(key string) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.Sub(l.lastSweep) >= limiterIdle {
		for k, e := range l.limiters {
			if now.Sub(e.seen) >= limiterIdle {
				delete(l.limiters, k)
			}
		}
		l.lastSweep = now
	}
	e, ok := l.limiters[key]
	if !ok {
		e = &limiterEntry{lim: rate.NewLimiter(l.rps, l.burst)}
		l.limiters[key] = e
	}
	e.seen = now
	return e.lim
}

// Middleware отвечает 429 с Retry-After при превышении лимита.
// Ключ — user:{id} после Authenticate, иначе ip:{адрес} из RemoteAddr; X-Forwarded-For не учитывается.
func (l *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var key string
		if id, ok := UserID(r.Context()); ok {
			key = "user:" + strconv.FormatUint(id, 10)
		} else {
			host, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				host = r.RemoteAddr
			}
			key = "ip:" + host
		}
		if !l.limiter(key).Allow() {
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, "too many requests")
			return
		}
		next.ServeHTTP(w, r)
	})
}
