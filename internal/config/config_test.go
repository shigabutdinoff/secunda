package config

import "testing"

func TestParsePriority(t *testing.T) {
	const dsn = "u:p@tcp(db:3306)/app"
	tests := []struct {
		name string
		env  map[string]string
		args []string
		want string
	}{
		{"default", nil, []string{"-d", dsn}, ":8080"},
		{"flag", nil, []string{"-d", dsn, "-a", ":9000"}, ":9000"},
		{"env beats flag", map[string]string{"ADDRESS": ":7000"}, []string{"-d", dsn, "-a", ":9000"}, ":7000"},
		{"empty env keeps flag", map[string]string{"ADDRESS": ""}, []string{"-d", dsn, "-a", ":9000"}, ":9000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("JWT_SECRET", "test-secret")
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			cfg, err := Parse(tt.args)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Address != tt.want {
				t.Errorf("Address = %q, want %q", cfg.Address, tt.want)
			}
		})
	}
}

func TestParseJWTSecret(t *testing.T) {
	const dsn = "u:p@tcp(db:3306)/app"
	tests := []struct {
		name string
		env  map[string]string
		args []string
		want string
	}{
		{"flag", nil, []string{"-d", dsn, "-j", "flag-secret"}, "flag-secret"},
		{"env beats flag", map[string]string{"JWT_SECRET": "env-secret"}, []string{"-d", dsn, "-j", "flag-secret"}, "env-secret"},
		{"empty env keeps flag", map[string]string{"JWT_SECRET": ""}, []string{"-d", dsn, "-j", "flag-secret"}, "flag-secret"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("JWT_SECRET", "")
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			cfg, err := Parse(tt.args)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.JWTSecret != tt.want {
				t.Errorf("JWTSecret = %q, want %q", cfg.JWTSecret, tt.want)
			}
		})
	}
}

func TestParseRequiresJWTSecret(t *testing.T) {
	t.Setenv("JWT_SECRET", "")
	_, err := Parse([]string{"-d", "u:p@tcp(db:3306)/app"})
	if err == nil || err.Error() != "JWT_SECRET не задан" {
		t.Fatalf("error = %v, want %q", err, "JWT_SECRET не задан")
	}
}

func TestParseRequiresDSN(t *testing.T) {
	t.Setenv("DATABASE_DSN", "")
	if _, err := Parse(nil); err == nil {
		t.Fatal("want error without DSN")
	}
}
