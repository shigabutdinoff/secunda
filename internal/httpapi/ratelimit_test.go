package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRateLimiter(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {})
	do := func(h http.Handler, addr string, userID uint64) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = addr
		if userID != 0 {
			r = r.WithContext(context.WithValue(r.Context(), userIDContextKey{}, userID))
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	t.Run("ip", func(t *testing.T) {
		h := NewRateLimiter(0.001, 2).Middleware(ok)
		for i := 0; i < 2; i++ {
			if w := do(h, "1.1.1.1:1000", 0); w.Code != http.StatusOK {
				t.Fatalf("request %d: status %d", i, w.Code)
			}
		}
		w := do(h, "1.1.1.1:2000", 0)
		if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "1" {
			t.Fatalf("status %d, Retry-After %q", w.Code, w.Header().Get("Retry-After"))
		}
		if w := do(h, "2.2.2.2:1000", 0); w.Code != http.StatusOK {
			t.Fatalf("other ip: status %d", w.Code)
		}
	})

	t.Run("user", func(t *testing.T) {
		h := NewRateLimiter(0.001, 1).Middleware(ok)
		if w := do(h, "1.1.1.1:1", 1); w.Code != http.StatusOK {
			t.Fatalf("user 1: status %d", w.Code)
		}
		if w := do(h, "1.1.1.1:1", 1); w.Code != http.StatusTooManyRequests {
			t.Fatalf("user 1 again: status %d", w.Code)
		}
		if w := do(h, "1.1.1.1:1", 2); w.Code != http.StatusOK {
			t.Fatalf("user 2: status %d", w.Code)
		}
	})
}
