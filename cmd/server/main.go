// Command server — REST API сервиса управления задачами. Настройки — в пакете config.
// При старте применяет миграции, останавливается по SIGTERM, SIGINT и SIGQUIT.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	_ "github.com/go-sql-driver/mysql"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/mysql"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/redis/go-redis/v9"

	"github.com/shigabutdinoff/secunda/internal/config"
	"github.com/shigabutdinoff/secunda/internal/httpapi"
)

const grace = 10 * time.Second

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT)
	defer stop()
	if err := run(ctx); err != nil {
		log.Fatal("Ошибка: ", err)
	}
}

func run(ctx context.Context) error {
	cfg, err := config.Parse(os.Args[1:])
	if err != nil {
		return err
	}

	db, err := sql.Open("mysql", cfg.DatabaseDSN)
	if err != nil {
		return fmt.Errorf("open mysql: %w", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(25)
	db.SetConnMaxLifetime(5 * time.Minute)
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		return fmt.Errorf("ping mysql: %w", err)
	}

	if err := migrateUp(cfg.MigrationsPath, cfg.DatabaseDSN); err != nil {
		return err
	}

	rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})
	defer func() { _ = rdb.Close() }()

	l, err := net.Listen("tcp", cfg.Address)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.Address, err)
	}
	srv := &http.Server{Handler: router(db, rdb, cfg.JWTSecret), ReadHeaderTimeout: 10 * time.Second}
	log.Printf("Сервер слушает %s", cfg.Address)
	return serve(ctx, srv, l, grace)
}

// migrateUp применяет миграции; драйвер сам включает multiStatements на своём соединении.
func migrateUp(path, dsn string) error {
	m, err := migrate.New("file://"+path, "mysql://"+dsn)
	if err != nil {
		return fmt.Errorf("init migrations: %w", err)
	}
	defer func() { _, _ = m.Close() }()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

func router(db *sql.DB, rdb *redis.Client, jwtSecret string) chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	handler := httpapi.NewHandler(db, rdb, jwtSecret)
	r.Post("/api/v1/register", handler.Register)
	r.Post("/api/v1/login", handler.Login)
	r.Route("/api/v1/tasks", func(r chi.Router) {
		r.Use(handler.Authenticate)
		r.Post("/", handler.CreateTask)
		r.Get("/", handler.ListTasks)
		r.Put("/{id}", handler.UpdateTask)
	})
	r.Route("/api/v1/teams", func(r chi.Router) {
		r.Use(handler.Authenticate)
		r.Post("/", handler.CreateTeam)
		r.Get("/", handler.ListTeams)
		r.Post("/{id}/invite", handler.InviteMember)
		r.Patch("/{id}/members/{user_id}", handler.UpdateMemberRole)
		r.Delete("/{id}/members/{user_id}", handler.RemoveMember)
	})
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := errors.Join(db.PingContext(ctx), rdb.Ping(ctx).Err()); err != nil {
			log.Print("health: ", err)
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok"))
	})
	return r
}

func serve(ctx context.Context, srv *http.Server, l net.Listener, grace time.Duration) error {
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(done)
		wait, cancel := context.WithTimeout(context.Background(), grace)
		defer cancel()
		if srv.Shutdown(wait) != nil {
			_ = srv.Close()
		}
	})
	defer stop()
	if err := srv.Serve(l); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-done
	return nil
}
