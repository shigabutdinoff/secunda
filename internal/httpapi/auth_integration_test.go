package httpapi

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

func TestRegisterLoginIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skip("TEST_DATABASE_DSN не задан")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.PingContext(t.Context()); err != nil {
		t.Fatalf("ping MySQL: %v", err)
	}

	h := NewHandler(db, testSecret)
	email := fmt.Sprintf("auth-%d@example.com", time.Now().UnixNano())
	defer func() { _, _ = db.ExecContext(t.Context(), "DELETE FROM users WHERE email = ?", email) }()
	body := fmt.Sprintf(`{"email":%q,"password":"correct-password","name":"Test User"}`, email)
	register := request(h.Register, http.MethodPost, "/api/v1/register", body)
	if register.Code != http.StatusCreated {
		t.Fatalf("register status = %d, body = %s; want 201", register.Code, register.Body)
	}
	var user map[string]any
	if err := json.Unmarshal(register.Body.Bytes(), &user); err != nil {
		t.Fatalf("decode register response: %v", err)
	}
	id, ok := user["id"].(float64)
	if len(user) != 3 || user["email"] != email || user["name"] != "Test User" || !ok || id <= 0 {
		t.Fatalf("register response = %#v; want exactly positive id, email and name", user)
	}
	for _, field := range []string{"password", "password_hash", "token"} {
		if _, ok := user[field]; ok {
			t.Errorf("register response contains %q", field)
		}
	}
	userID := uint64(id)
	var passwordHash []byte
	if err := db.QueryRowContext(t.Context(), "SELECT password_hash FROM users WHERE id = ?", userID).Scan(&passwordHash); err != nil {
		t.Fatalf("read password hash: %v", err)
	}
	if cost, err := bcrypt.Cost(passwordHash); err != nil || cost != bcrypt.DefaultCost {
		t.Fatalf("bcrypt cost = %d, error = %v; want %d", cost, err, bcrypt.DefaultCost)
	}

	duplicate := request(h.Register, http.MethodPost, "/api/v1/register", body)
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate status = %d, body = %s; want 409", duplicate.Code, duplicate.Body)
	}

	unknown := request(h.Login, http.MethodPost, "/api/v1/login", `{"email":"unknown@example.com","password":"correct-password"}`)
	wrongPassword := request(h.Login, http.MethodPost, "/api/v1/login", fmt.Sprintf(`{"email":%q,"password":"wrong-password"}`, email))
	if unknown.Code != http.StatusUnauthorized || wrongPassword.Code != http.StatusUnauthorized || unknown.Body.String() != wrongPassword.Body.String() {
		t.Fatalf("unknown = (%d, %q), wrong password = (%d, %q); want identical 401 responses", unknown.Code, unknown.Body, wrongPassword.Code, wrongPassword.Body)
	}

	login := request(h.Login, http.MethodPost, "/api/v1/login", fmt.Sprintf(`{"email":%q,"password":"correct-password"}`, email))
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, body = %s; want 200", login.Code, login.Body)
	}
	var response struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &response); err != nil || response.Token == "" {
		t.Fatalf("login response = %q, error = %v; want token", login.Body, err)
	}
	var claims jwtClaims
	token, err := jwt.ParseWithClaims(response.Token, &claims, func(token *jwt.Token) (any, error) {
		return []byte(testSecret), nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil || !token.Valid || claims.ExpiresAt == nil || claims.UserID != userID {
		t.Fatalf("token = %#v, claims = %#v, error = %v; want valid token with exp and user ID %d", token, claims, err, userID)
	}

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+response.Token)
	rec := httptest.NewRecorder()
	h.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := UserID(r.Context())
		if !ok || got != userID {
			t.Errorf("UserID() = (%d, %v), want (%d, true)", got, ok, userID)
		}
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("protected status = %d, body = %s; want 204", rec.Code, rec.Body)
	}

	t.Run("eight space password", func(t *testing.T) {
		email := fmt.Sprintf("spaces-%d@example.com", time.Now().UnixNano())
		defer func() { _, _ = db.ExecContext(t.Context(), "DELETE FROM users WHERE email = ?", email) }()
		body := fmt.Sprintf(`{"email":%q,"password":"        ","name":"Space User"}`, email)
		register := request(h.Register, http.MethodPost, "/api/v1/register", body)
		if register.Code != http.StatusCreated {
			t.Fatalf("register status = %d, body = %s; want 201", register.Code, register.Body)
		}
		login := request(h.Login, http.MethodPost, "/api/v1/login", fmt.Sprintf(`{"email":%q,"password":"        "}`, email))
		if login.Code != http.StatusOK {
			t.Fatalf("login status = %d, body = %s; want 200", login.Code, login.Body)
		}
	})

	t.Run("eight character non-ASCII password", func(t *testing.T) {
		email := fmt.Sprintf("unicode-%d@example.com", time.Now().UnixNano())
		defer func() { _, _ = db.ExecContext(t.Context(), "DELETE FROM users WHERE email = ?", email) }()
		body := fmt.Sprintf(`{"email":%q,"password":"пароль!!","name":"Unicode User"}`, email)
		register := request(h.Register, http.MethodPost, "/api/v1/register", body)
		if register.Code != http.StatusCreated {
			t.Fatalf("register status = %d, body = %s; want 201", register.Code, register.Body)
		}
		login := request(h.Login, http.MethodPost, "/api/v1/login", fmt.Sprintf(`{"email":%q,"password":"пароль!!"}`, email))
		if login.Code != http.StatusOK {
			t.Fatalf("login status = %d, body = %s; want 200", login.Code, login.Body)
		}
	})
}

func request(handler http.HandlerFunc, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}
