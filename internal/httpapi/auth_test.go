package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const testSecret = "test-secret"

func TestIssueToken(t *testing.T) {
	now := time.Date(2026, time.October, 27, 12, 0, 0, 0, time.UTC)
	h := NewHandler(nil, testSecret)
	h.now = func() time.Time { return now }

	tokenString, err := h.issueToken(42)
	if err != nil {
		t.Fatalf("issueToken() error = %v", err)
	}

	var claims jwtClaims
	token, err := jwt.ParseWithClaims(tokenString, &claims, func(token *jwt.Token) (any, error) {
		return []byte(testSecret), nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil {
		t.Fatalf("ParseWithClaims() error = %v", err)
	}
	if !token.Valid || claims.UserID != 42 {
		t.Fatalf("token.Valid = %v, UserID = %d; want true, 42", token.Valid, claims.UserID)
	}
	if !claims.IssuedAt.Equal(now) {
		t.Errorf("iat = %v, want %v", claims.IssuedAt.Time, now)
	}
	if !claims.ExpiresAt.Equal(now.Add(24 * time.Hour)) {
		t.Errorf("exp = %v, want %v", claims.ExpiresAt.Time, now.Add(24*time.Hour))
	}
}

func TestAuthenticate(t *testing.T) {
	now := time.Date(2026, time.October, 27, 12, 0, 0, 0, time.UTC)
	h := NewHandler(nil, testSecret)
	h.now = func() time.Time { return now }
	validToken, err := h.issueToken(42)
	if err != nil {
		t.Fatalf("issueToken() error = %v", err)
	}

	sign := func(method jwt.SigningMethod, userID uint64, issuedAt, expiresAt time.Time) string {
		t.Helper()
		token := jwt.NewWithClaims(method, jwtClaims{
			RegisteredClaims: jwt.RegisteredClaims{
				IssuedAt:  jwt.NewNumericDate(issuedAt),
				ExpiresAt: jwt.NewNumericDate(expiresAt),
			},
			UserID: userID,
		})
		var key any = []byte(testSecret)
		if method == jwt.SigningMethodNone {
			key = jwt.UnsafeAllowNoneSignatureType
		}
		tokenString, err := token.SignedString(key)
		if err != nil {
			t.Fatalf("SignedString(%s) error = %v", method.Alg(), err)
		}
		return tokenString
	}

	tests := []struct {
		name          string
		authorization string
		wantStatus    int
		wantUserID    uint64
		wantNext      bool
	}{
		{name: "valid token", authorization: "Bearer " + validToken, wantStatus: http.StatusOK, wantUserID: 42, wantNext: true},
		{name: "case insensitive bearer", authorization: "bearer " + validToken, wantStatus: http.StatusOK, wantUserID: 42, wantNext: true},
		{name: "missing token", wantStatus: http.StatusUnauthorized},
		{name: "not bearer", authorization: "Basic " + validToken, wantStatus: http.StatusUnauthorized},
		{name: "malformed token", authorization: "Bearer broken.token.value", wantStatus: http.StatusUnauthorized},
		{name: "extra authorization part", authorization: "Bearer " + validToken + " extra", wantStatus: http.StatusUnauthorized},
		{name: "expired token", authorization: "Bearer " + sign(jwt.SigningMethodHS256, 42, now.Add(-2*time.Hour), now.Add(-time.Hour)), wantStatus: http.StatusUnauthorized},
		{name: "alg none", authorization: "Bearer " + sign(jwt.SigningMethodNone, 42, now, now.Add(24*time.Hour)), wantStatus: http.StatusUnauthorized},
		{name: "HS384", authorization: "Bearer " + sign(jwt.SigningMethodHS384, 42, now, now.Add(24*time.Hour)), wantStatus: http.StatusUnauthorized},
		{name: "zero user ID", authorization: "Bearer " + sign(jwt.SigningMethodHS256, 0, now, now.Add(24*time.Hour)), wantStatus: http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nextCalled := false
			var gotUserID uint64
			var hasUserID bool
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				nextCalled = true
				gotUserID, hasUserID = UserID(r.Context())
				w.WriteHeader(http.StatusOK)
			})
			req := httptest.NewRequest(http.MethodGet, "/protected", nil)
			req.Header.Set("Authorization", tt.authorization)
			rec := httptest.NewRecorder()

			h.Authenticate(next).ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if nextCalled != tt.wantNext {
				t.Errorf("next called = %v, want %v", nextCalled, tt.wantNext)
			}
			if tt.wantNext && (!hasUserID || gotUserID != tt.wantUserID) {
				t.Errorf("UserID() = (%d, %v), want (%d, true)", gotUserID, hasUserID, tt.wantUserID)
			}
		})
	}
}

func TestRegisterValidation(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "malformed JSON", body: `{"email":`},
		{name: "trailing JSON", body: `{"email":"user@example.com","password":"password","name":"User"} {}`},
		{name: "empty email", body: `{"email":"","password":"password","name":"User"}`},
		{name: "whitespace email", body: `{"email":"  ","password":"password","name":"User"}`},
		{name: "empty password", body: `{"email":"user@example.com","password":"","name":"User"}`},
		{name: "whitespace password", body: `{"email":"user@example.com","password":"  ","name":"User"}`},
		{name: "short password", body: `{"email":"user@example.com","password":"short","name":"User"}`},
		{name: "seven character non-ASCII password", body: `{"email":"user@example.com","password":"пароль!","name":"User"}`},
		{name: "empty name", body: `{"email":"user@example.com","password":"password","name":""}`},
		{name: "whitespace name", body: `{"email":"user@example.com","password":"password","name":"  "}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHandler(nil, testSecret)
			rec := httptest.NewRecorder()
			h.Register(rec, httptest.NewRequest(http.MethodPost, "/api/v1/register", strings.NewReader(tt.body)))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s; want 400", rec.Code, rec.Body)
			}
		})
	}
}

func TestLoginValidation(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "malformed JSON", body: `{"email":`},
		{name: "trailing JSON", body: `{"email":"user@example.com","password":"password"} {}`},
		{name: "empty email", body: `{"email":"","password":"password"}`},
		{name: "whitespace email", body: `{"email":"  ","password":"password"}`},
		{name: "empty password", body: `{"email":"user@example.com","password":""}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHandler(nil, testSecret)
			rec := httptest.NewRecorder()
			h.Login(rec, httptest.NewRequest(http.MethodPost, "/api/v1/login", strings.NewReader(tt.body)))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s; want 400", rec.Code, rec.Body)
			}
		})
	}
}

func TestUserIDWithoutAuthentication(t *testing.T) {
	if userID, ok := UserID(t.Context()); ok || userID != 0 {
		t.Fatalf("UserID() = (%d, %v), want (0, false)", userID, ok)
	}
}

func TestWriteJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusCreated, map[string]bool{"ok": true})

	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusCreated)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if got := rec.Body.String(); got != "{\"ok\":true}\n" {
		t.Errorf("body = %q, want %q", got, "{\"ok\":true}\n")
	}
}

func TestWriteError(t *testing.T) {
	rec := httptest.NewRecorder()
	writeError(rec, http.StatusBadRequest, "bad request")

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if got := rec.Body.String(); got != "{\"error\":\"bad request\"}\n" {
		t.Errorf("body = %q, want %q", got, "{\"error\":\"bad request\"}\n")
	}
}
