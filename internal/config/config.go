// Package config читает настройки сервиса: окружение важнее флагов, флаги важнее значений по умолчанию.
package config

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/caarlos0/env/v11"
)

// Config — настройки сервиса.
type Config struct {
	Address        string `env:"ADDRESS"`
	DatabaseDSN    string `env:"DATABASE_DSN"`
	RedisAddr      string `env:"REDIS_ADDR"`
	MigrationsPath string `env:"MIGRATIONS_PATH"`
}

// Parse разбирает флаги args, затем переопределяет их непустыми переменными окружения.
func Parse(args []string) (Config, error) {
	var cfg Config
	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	fs.StringVar(&cfg.Address, "a", ":8080", "адрес HTTP-сервера")
	fs.StringVar(&cfg.DatabaseDSN, "d", "", "DSN MySQL")
	fs.StringVar(&cfg.RedisAddr, "r", "localhost:6379", "адрес Redis")
	fs.StringVar(&cfg.MigrationsPath, "m", "migrations", "каталог миграций")
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
	return cfg, nil
}
