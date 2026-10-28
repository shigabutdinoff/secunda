package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"
)

type requestLogKey struct{}

// requestLog — логгер запроса и ошибка, из-за которой ответили 5xx.
type requestLog struct {
	logger *zap.Logger
	err    error
}

type loggingResponseWriter struct {
	http.ResponseWriter
	status int
	size   int
}

// Write запоминает размер ответа и статус 200, если WriteHeader не вызывался.
func (w *loggingResponseWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.size += n
	return n, err
}

// WriteHeader запоминает первый записанный статус.
func (w *loggingResponseWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

// WithLogging пишет в журнал request_id (от middleware.RequestID), uri, method, status, size и duration каждого запроса.
// Ответы 5xx пишет на уровне Error с ошибкой, переданной в internalError.
func WithLogging(logger *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			reqLogger := logger
			if id := middleware.GetReqID(r.Context()); id != "" {
				reqLogger = logger.With(zap.String("request_id", id))
			}
			rl := &requestLog{logger: reqLogger}
			lw := &loggingResponseWriter{ResponseWriter: w}
			next.ServeHTTP(lw, r.WithContext(context.WithValue(r.Context(), requestLogKey{}, rl)))
			if lw.status == 0 {
				lw.status = http.StatusOK
			}
			fields := []zap.Field{
				zap.String("uri", r.URL.Path),
				zap.String("method", r.Method),
				zap.Int("status", lw.status),
				zap.Int("size", lw.size),
				zap.Duration("duration", time.Since(start)),
			}
			if lw.status >= http.StatusInternalServerError {
				reqLogger.Error("Сведения о запросе", append(fields, zap.Error(rl.err))...)
				return
			}
			reqLogger.Info("Сведения о запросе", fields...)
		})
	}
}

// loggerFrom возвращает логгер запроса или пустой, если WithLogging не подключён.
func loggerFrom(ctx context.Context) *zap.Logger {
	if rl, ok := ctx.Value(requestLogKey{}).(*requestLog); ok {
		return rl.logger
	}
	return zap.NewNop()
}

// internalError запоминает err для журнала и отвечает клиенту 500 без деталей.
func internalError(w http.ResponseWriter, r *http.Request, err error) {
	if rl, ok := r.Context().Value(requestLogKey{}).(*requestLog); ok {
		rl.err = err
	}
	writeError(w, http.StatusInternalServerError, "internal error")
}
