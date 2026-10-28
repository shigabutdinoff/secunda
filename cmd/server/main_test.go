package main

import (
	"context"
	"database/sql"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// dead — адрес, где гарантированно никто не слушает.
const dead = "127.0.0.1:1"

func TestRunRequiresDSN(t *testing.T) {
	t.Setenv("DATABASE_DSN", "")
	os.Args = []string{"server"}
	if err := run(context.Background()); err == nil {
		t.Fatal("ожидалась ошибка без DSN")
	}
}

func TestRunMySQLUnavailable(t *testing.T) {
	t.Setenv("DATABASE_DSN", "u:p@tcp("+dead+")/app?timeout=1s")
	t.Setenv("JWT_SECRET", "test-secret")
	os.Args = []string{"server"}
	if err := run(context.Background()); err == nil {
		t.Fatal("ожидалась ошибка пинга MySQL")
	}
}

func TestMigrateUpBadPath(t *testing.T) {
	if err := migrateUp(t.TempDir()+"/missing", "u:p@tcp("+dead+")/app"); err == nil {
		t.Fatal("ожидалась ошибка инициализации миграций")
	}
}

func TestHealthUnavailable(t *testing.T) {
	db, err := sql.Open("mysql", "u:p@tcp("+dead+")/app?timeout=1s")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	rdb := redis.NewClient(&redis.Options{Addr: dead})
	defer func() { _ = rdb.Close() }()

	rec := httptest.NewRecorder()
	router(db, rdb, "test-secret").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", rec.Code)
	}
}

func TestAuthRoutes(t *testing.T) {
	r := router(nil, nil, "test-secret")
	for _, path := range []string{"/api/v1/register", "/api/v1/login"} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"email":`)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("POST %s status = %d, body = %s; want 400", path, rec.Code, rec.Body)
		}
	}
}

func TestTeamRoutesRequireAuthentication(t *testing.T) {
	r := router(nil, nil, "test-secret")
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/teams"},
		{http.MethodGet, "/api/v1/teams"},
		{http.MethodPost, "/api/v1/teams/1/invite"},
		{http.MethodPatch, "/api/v1/teams/1/members/2"},
		{http.MethodDelete, "/api/v1/teams/1/members/2"},
		{http.MethodPost, "/api/v1/tasks"},
		{http.MethodGet, "/api/v1/tasks"},
	} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s status = %d; want 401", tc.method, tc.path, rec.Code)
		}
	}
}

// TestRunServesAndStops нужны живые MySQL и Redis: TEST_DATABASE_DSN и TEST_REDIS_ADDR.
func TestRunServesAndStops(t *testing.T) {
	dsn, redisAddr := os.Getenv("TEST_DATABASE_DSN"), os.Getenv("TEST_REDIS_ADDR")
	if dsn == "" || redisAddr == "" {
		t.Skip("TEST_DATABASE_DSN или TEST_REDIS_ADDR не заданы")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	t.Setenv("ADDRESS", addr)
	t.Setenv("DATABASE_DSN", dsn)
	t.Setenv("JWT_SECRET", "test-secret")
	t.Setenv("REDIS_ADDR", redisAddr)
	t.Setenv("MIGRATIONS_PATH", "../../migrations")
	os.Args = []string{"server"}

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- run(ctx) }()

	deadline := time.Now().Add(30 * time.Second)
	for {
		resp, err := http.Get("http://" + addr + "/health")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("health не ответил 200: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	cancel()
	if err := <-errc; err != nil {
		t.Fatalf("run = %v, want nil", err)
	}
}

func TestServeStopsOnCancel(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- serve(ctx, &http.Server{Handler: http.NotFoundHandler()}, l, time.Second) }()
	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("serve = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve не остановился")
	}
}
