package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestWithLogging(t *testing.T) {
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		status  int
		size    int
		level   zapcore.Level
		err     string
	}{
		{name: "ok", handler: func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("hello")) }, status: 200, size: 5, level: zap.InfoLevel},
		{name: "not found", handler: func(w http.ResponseWriter, _ *http.Request) { writeError(w, 404, "x") }, status: 404, size: 14, level: zap.InfoLevel},
		{name: "internal", handler: func(w http.ResponseWriter, r *http.Request) { internalError(w, r, errors.New("db down")) }, status: 500, size: 27, level: zap.ErrorLevel, err: "db down"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core, logs := observer.New(zap.InfoLevel)
			rec := httptest.NewRecorder()
			WithLogging(zap.New(core))(tc.handler).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/tasks?token=secret", nil))
			if logs.Len() != 1 {
				t.Fatalf("entries = %d, want 1", logs.Len())
			}
			entry := logs.All()[0]
			fields := entry.ContextMap()
			if entry.Level != tc.level || fields["status"] != int64(tc.status) || fields["size"] != int64(tc.size) || fields["uri"] != "/api/v1/tasks" {
				t.Fatalf("entry = %v %v", entry.Level, fields)
			}
			if got, _ := fields["error"].(string); got != tc.err {
				t.Fatalf("error = %q, want %q", got, tc.err)
			}
			if rec.Body.Len() != tc.size {
				t.Fatalf("body size = %d, want %d", rec.Body.Len(), tc.size)
			}
		})
	}
}
