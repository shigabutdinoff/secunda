package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestCreateTeamValidation(t *testing.T) {
	for _, body := range []string{`{"name":`, `{"name":"Team"} {}`, `{"name":""}`, `{"name":"  "}`} {
		req := authenticatedTeamRequest(http.MethodPost, "/api/v1/teams", body, "1")
		rec := httptest.NewRecorder()
		NewHandler(nil, nil, testSecret).CreateTeam(rec, req)
		wantJSON(t, rec)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %q: status = %d, want 400", body, rec.Code)
		}
	}
}

func TestInviteMemberValidation(t *testing.T) {
	for _, body := range []string{
		`{"email":`, `{"email":"user@example.com","role":"member"} {}`,
		`{"email":"","role":"member"}`, `{"email":"user@example.com","role":"owner"}`,
		`{"email":"user@example.com","role":"unknown"}`,
	} {
		req := authenticatedTeamRequest(http.MethodPost, "/api/v1/teams/1/invite", body, "1")
		rec := httptest.NewRecorder()
		NewHandler(nil, nil, testSecret).InviteMember(rec, req)
		wantJSON(t, rec)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %q: status = %d, want 400", body, rec.Code)
		}
	}
}

func TestInviteMemberRejectsInvalidTeamID(t *testing.T) {
	req := authenticatedTeamRequest(http.MethodPost, "/api/v1/teams/bad/invite", `{"email":"user@example.com","role":"member"}`, "bad")
	rec := httptest.NewRecorder()
	NewHandler(nil, nil, testSecret).InviteMember(rec, req)
	wantJSON(t, rec)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func authenticatedTeamRequest(method, path, body, teamID string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), userIDContextKey{}, uint64(42)))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", teamID)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func TestMemberEndpointsValidation(t *testing.T) {
	for _, tc := range []struct{ teamID, userID, body string }{
		{"1", "2", `{"role":"owner"}`},
		{"1", "2", `{"role":`},
		{"bad", "2", `{"role":"admin"}`},
		{"1", "0", `{"role":"admin"}`},
	} {
		req := authenticatedTeamRequest(http.MethodPatch, "/api/v1/teams/members", tc.body, tc.teamID)
		chi.RouteContext(req.Context()).URLParams.Add("user_id", tc.userID)
		rec := httptest.NewRecorder()
		NewHandler(nil, nil, testSecret).UpdateMemberRole(rec, req)
		wantJSON(t, rec)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%+v: status = %d, want 400", tc, rec.Code)
		}
	}
	req := authenticatedTeamRequest(http.MethodDelete, "/api/v1/teams/1/members/x", "", "1")
	chi.RouteContext(req.Context()).URLParams.Add("user_id", "x")
	rec := httptest.NewRecorder()
	NewHandler(nil, nil, testSecret).RemoveMember(rec, req)
	wantJSON(t, rec)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("remove status = %d, want 400", rec.Code)
	}
}

// wantJSON проверяет, что ответ отдан как JSON.
func wantJSON(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
}
