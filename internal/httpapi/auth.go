// Package httpapi реализует HTTP API сервиса управления задачами.
package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"
	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
)

// Handler содержит зависимости HTTP-обработчиков.
type Handler struct {
	db        *sql.DB
	cache     *redis.Client
	jwtSecret []byte
	now       func() time.Time
}

// NewHandler создаёт Handler с базой данных, Redis и секретом JWT.
func NewHandler(db *sql.DB, cache *redis.Client, jwtSecret string) *Handler {
	return &Handler{db: db, cache: cache, jwtSecret: []byte(jwtSecret), now: time.Now}
}

type authRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
}

func decodeAuthRequest(w http.ResponseWriter, r *http.Request) (authRequest, bool) {
	var input authRequest
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&input); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request")
		return authRequest{}, false
	}
	return input, true
}

// Register создаёт пользователя с хешированным паролем.
func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
	input, ok := decodeAuthRequest(w, r)
	if !ok {
		return
	}
	if strings.TrimSpace(input.Email) == "" || strings.TrimSpace(input.Name) == "" ||
		input.Password == "" || utf8.RuneCountInString(input.Password) < 8 {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	result, err := h.db.ExecContext(r.Context(),
		"INSERT INTO users (email, password_hash, name) VALUES (?, ?, ?)", input.Email, hash, input.Name)
	if err != nil {
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
			writeError(w, http.StatusConflict, "email already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	id, err := result.LastInsertId()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "email": input.Email, "name": input.Name})
}

// Login проверяет учётные данные и выдаёт JWT.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	input, ok := decodeAuthRequest(w, r)
	if !ok {
		return
	}
	if strings.TrimSpace(input.Email) == "" || input.Password == "" {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	var id uint64
	var hash string
	err := h.db.QueryRowContext(r.Context(),
		"SELECT id, password_hash FROM users WHERE email = ?", input.Email).Scan(&id, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(input.Password)); err != nil {
		if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
			writeError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	token, err := h.issueToken(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

type jwtClaims struct {
	jwt.RegisteredClaims
	UserID uint64 `json:"user_id"`
}

func (h *Handler) issueToken(userID uint64) (string, error) {
	now := h.now()
	claims := jwtClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(24 * time.Hour)),
		},
		UserID: userID,
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(h.jwtSecret)
}

type userIDContextKey struct{}

// Authenticate проверяет JWT и передаёт идентификатор пользователя следующему обработчику.
func (h *Handler) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Fields(r.Header.Get("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		var claims jwtClaims
		token, err := jwt.ParseWithClaims(parts[1], &claims, func(token *jwt.Token) (any, error) {
			if token.Method != jwt.SigningMethodHS256 {
				return nil, jwt.ErrTokenSignatureInvalid
			}
			return h.jwtSecret, nil
		}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
		if err != nil || !token.Valid || claims.UserID == 0 {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		ctx := context.WithValue(r.Context(), userIDContextKey{}, claims.UserID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// UserID возвращает идентификатор аутентифицированного пользователя из контекста.
func UserID(ctx context.Context) (uint64, bool) {
	userID, ok := ctx.Value(userIDContextKey{}).(uint64)
	return userID, ok
}
