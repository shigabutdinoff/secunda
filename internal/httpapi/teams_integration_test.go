package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-sql-driver/mysql"
)

func TestTeamsIntegration(t *testing.T) {
	db := openTeamTestDB(t)
	h := NewHandler(db, testSecret)
	r := chi.NewRouter()
	r.Route("/api/v1/teams", func(r chi.Router) {
		r.Use(h.Authenticate)
		r.Post("/", h.CreateTeam)
		r.Get("/", h.ListTeams)
		r.Post("/{id}/invite", h.InviteMember)
		r.Patch("/{id}/members/{user_id}", h.UpdateMemberRole)
		r.Delete("/{id}/members/{user_id}", h.RemoveMember)
	})

	owner := createTeamTestUser(t, db, "owner")
	admin := createTeamTestUser(t, db, "admin")
	member := createTeamTestUser(t, db, "member")
	outsider := createTeamTestUser(t, db, "outsider")
	defer func() {
		_, _ = db.ExecContext(t.Context(), "DELETE FROM users WHERE id IN (?, ?, ?, ?)", owner, admin, member, outsider)
	}()

	teamName := fmt.Sprintf("Team %d", time.Now().UnixNano())
	created := teamAPIRequest(t, h, r, owner, http.MethodPost, "/api/v1/teams", fmt.Sprintf(`{"name":%q}`, teamName))
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s; want 201", created.Code, created.Body)
	}
	var team struct {
		ID uint64 `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &team); err != nil || team.ID == 0 {
		t.Fatalf("create response = %s, error = %v", created.Body, err)
	}
	var role string
	if err := db.QueryRowContext(t.Context(), "SELECT role FROM team_members WHERE team_id = ? AND user_id = ?", team.ID, owner).Scan(&role); err != nil || role != "owner" {
		t.Fatalf("owner role = %q, error = %v; want owner", role, err)
	}

	empty := teamAPIRequest(t, h, r, outsider, http.MethodGet, "/api/v1/teams", "")
	if empty.Code != http.StatusOK || strings.TrimSpace(empty.Body.String()) != "[]" {
		t.Fatalf("empty list = (%d, %q); want (200, [])", empty.Code, empty.Body)
	}
	ownerList := teamAPIRequest(t, h, r, owner, http.MethodGet, "/api/v1/teams", "")
	if ownerList.Code != http.StatusOK || !strings.Contains(ownerList.Body.String(), teamName) {
		t.Fatalf("owner list = (%d, %s)", ownerList.Code, ownerList.Body)
	}

	invite := func(caller uint64, email, role string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"email":%q,"role":%q}`, email, role)
		return teamAPIRequest(t, h, r, caller, http.MethodPost, fmt.Sprintf("/api/v1/teams/%d/invite", team.ID), body)
	}
	email := func(userID uint64) string {
		var value string
		if err := db.QueryRowContext(t.Context(), "SELECT email FROM users WHERE id = ?", userID).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	if rec := invite(owner, email(admin), "admin"); rec.Code != http.StatusCreated {
		t.Fatalf("owner invite admin = (%d, %s); want 201", rec.Code, rec.Body)
	}
	if rec := invite(admin, email(member), "member"); rec.Code != http.StatusCreated {
		t.Fatalf("admin invite member = (%d, %s); want 201", rec.Code, rec.Body)
	}
	if rec := invite(member, email(outsider), "member"); rec.Code != http.StatusForbidden {
		t.Fatalf("member invite = (%d, %s); want 403", rec.Code, rec.Body)
	}
	if rec := invite(outsider, email(member), "member"); rec.Code != http.StatusNotFound {
		t.Fatalf("outsider invite = (%d, %s); want 404", rec.Code, rec.Body)
	}
	if rec := invite(owner, "missing@example.com", "member"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown email = (%d, %s); want 404", rec.Code, rec.Body)
	}
	if rec := invite(owner, email(member), "member"); rec.Code != http.StatusConflict {
		t.Fatalf("duplicate = (%d, %s); want 409", rec.Code, rec.Body)
	}
	memberPath := func(userID uint64) string { return fmt.Sprintf("/api/v1/teams/%d/members/%d", team.ID, userID) }
	for _, tc := range []struct {
		name           string
		caller, target uint64
		method, body   string
		want           int
	}{
		{"admin меняет роль", admin, member, http.MethodPatch, `{"role":"admin"}`, http.StatusForbidden},
		{"owner меняет свою роль", owner, owner, http.MethodPatch, `{"role":"member"}`, http.StatusForbidden},
		{"admin удаляет owner", admin, owner, http.MethodDelete, "", http.StatusForbidden},
		{"member удаляет member", member, member, http.MethodDelete, "", http.StatusForbidden},
		{"цель не в команде", owner, outsider, http.MethodPatch, `{"role":"admin"}`, http.StatusNotFound},
		{"outsider меняет роль", outsider, member, http.MethodPatch, `{"role":"admin"}`, http.StatusNotFound},
		{"owner повышает member", owner, member, http.MethodPatch, `{"role":"admin"}`, http.StatusOK},
		{"admin удаляет admin", admin, member, http.MethodDelete, "", http.StatusForbidden},
		{"owner понижает admin", owner, member, http.MethodPatch, `{"role":"member"}`, http.StatusOK},
	} {
		if rec := teamAPIRequest(t, h, r, tc.caller, tc.method, memberPath(tc.target), tc.body); rec.Code != tc.want {
			t.Fatalf("%s = (%d, %s); want %d", tc.name, rec.Code, rec.Body, tc.want)
		}
	}
	if _, err := db.ExecContext(t.Context(), "INSERT INTO tasks (team_id, title, description, created_by, assignee_id) VALUES (?, 't', '', ?, ?)", team.ID, owner, member); err != nil {
		t.Fatal(err)
	}
	if rec := teamAPIRequest(t, h, r, admin, http.MethodDelete, memberPath(member), ""); rec.Code != http.StatusConflict {
		t.Fatalf("удаление исполнителя = (%d, %s); want 409", rec.Code, rec.Body)
	}
	if _, err := db.ExecContext(t.Context(), "DELETE FROM tasks WHERE team_id = ?", team.ID); err != nil {
		t.Fatal(err)
	}
	memberList := teamAPIRequest(t, h, r, member, http.MethodGet, "/api/v1/teams", "")
	if !strings.Contains(memberList.Body.String(), teamName) {
		t.Fatalf("member list = %s; want invited team", memberList.Body)
	}

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/teams", `{"name":"No auth"}`},
		{http.MethodGet, "/api/v1/teams", ""},
		{http.MethodPost, fmt.Sprintf("/api/v1/teams/%d/invite", team.ID), `{"email":"user@example.com","role":"member"}`},
	} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without JWT = %d; want 401", tc.method, tc.path, rec.Code)
		}
	}
}

func TestCreateTeamRollbackIntegration(t *testing.T) {
	db := openTeamTestDB(t)
	h := NewHandler(db, testSecret)
	owner := createTeamTestUser(t, db, "rollback")
	defer func() { _, _ = db.ExecContext(t.Context(), "DELETE FROM users WHERE id = ?", owner) }()
	trigger := fmt.Sprintf("reject_team_member_%d", time.Now().UnixNano())
	if _, err := db.ExecContext(t.Context(), fmt.Sprintf("CREATE TRIGGER %s BEFORE INSERT ON team_members FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'forced failure'", trigger)); err != nil {
		if mysqlErr, ok := err.(*mysql.MySQLError); ok && mysqlErr.Number == 1419 {
			t.Skip("создание trigger запрещено настройками MySQL")
		}
		t.Fatalf("create trigger: %v", err)
	}
	defer func() { _, _ = db.ExecContext(t.Context(), "DROP TRIGGER "+trigger) }()
	teamName := fmt.Sprintf("Rollback %d", time.Now().UnixNano())
	req := authenticatedTeamRequest(http.MethodPost, "/api/v1/teams", fmt.Sprintf(`{"name":%q}`, teamName), "1")
	req = req.WithContext(contextWithUserID(req, owner))
	rec := httptest.NewRecorder()
	h.CreateTeam(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s; want 500", rec.Code, rec.Body)
	}
	var count int
	if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM teams WHERE name = ?", teamName).Scan(&count); err != nil || count != 0 {
		t.Fatalf("teams after rollback = %d, error = %v; want 0", count, err)
	}
}

func openTeamTestDB(t *testing.T) *sql.DB {
	t.Helper()
	return openIntegrationTestDB(t)
}

func createTeamTestUser(t *testing.T, db *sql.DB, prefix string) uint64 {
	t.Helper()
	email := fmt.Sprintf("%s-%d@example.com", prefix, time.Now().UnixNano())
	result, err := db.ExecContext(t.Context(), "INSERT INTO users (email, password_hash, name) VALUES (?, 'unused', ?)", email, prefix)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return uint64(id)
}

func teamAPIRequest(t *testing.T, h *Handler, router http.Handler, userID uint64, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	token, err := h.issueToken(userID)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func contextWithUserID(req *http.Request, userID uint64) context.Context {
	return context.WithValue(req.Context(), userIDContextKey{}, userID)
}
