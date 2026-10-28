// Package config читает настройки сервиса: окружение важнее флагов, флаги важнее значений по умолчанию.
package config

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/caarlos0/env/v11"
)

// Config — настройки сервиса.
type Config struct {
	Address     string `env:"ADDRESS"`
	DatabaseDSN string `env:"DATABASE_DSN"`
	// JWTSecret — секрет для подписи JWT.
	JWTSecret string `env:"JWT_SECRET"`
	RedisAddr string `env:"REDIS_ADDR"`
	// RedisBreakerFailures — сбоев Redis подряд до размыкания breaker.
	RedisBreakerFailures uint `env:"REDIS_BREAKER_FAILURES"`
	// RedisBreakerTimeout — сколько breaker держится разомкнутым до пробного вызова.
	RedisBreakerTimeout time.Duration `env:"REDIS_BREAKER_TIMEOUT"`
	MigrationsPath      string        `env:"MIGRATIONS_PATH"`
	// LogLevel — уровень журнала zap: debug, info, warn, error.
	LogLevel string `env:"LOG_LEVEL"`
}

// Parse разбирает флаги args, затем переопределяет их непустыми переменными окружения.
func Parse(args []string) (Config, error) {
	var cfg Config
	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	fs.StringVar(&cfg.Address, "a", ":8080", "адрес HTTP-сервера")
	fs.StringVar(&cfg.DatabaseDSN, "d", "", "DSN MySQL")
	fs.StringVar(&cfg.JWTSecret, "j", "", "секрет JWT")
	fs.StringVar(&cfg.RedisAddr, "r", "localhost:6379", "адрес Redis")
	fs.UintVar(&cfg.RedisBreakerFailures, "rf", 5, "сбоев Redis подряд до размыкания breaker")
	fs.DurationVar(&cfg.RedisBreakerTimeout, "rt", 30*time.Second, "время до пробного вызова Redis")
	fs.StringVar(&cfg.MigrationsPath, "m", "migrations", "каталог миграций")
	fs.StringVar(&cfg.LogLevel, "l", "info", "уровень журнала")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}

	// пустая переменная (compose подставляет "" для незаданного ${VAR}) не затирает флаг
	vars := env.ToMap(os.Environ())
	for k, v := range vars {
		if v == "" {
			delete(vars, k)
		}
	}
	if err := env.ParseWithOptions(&cfg, env.Options{Environment: vars}); err != nil {
		return Config{}, fmt.Errorf("parse environment: %w", err)
	}

	if cfg.DatabaseDSN == "" {
		return Config{}, errors.New("DATABASE_DSN не задан")
	}
	if cfg.JWTSecret == "" {
		return Config{}, errors.New("JWT_SECRET не задан")
	}
	return cfg, nil
}
