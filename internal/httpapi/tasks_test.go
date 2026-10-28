package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCreateTaskValidation(t *testing.T) {
	for _, body := range []string{
		`{"team_id":`,
		`{"team_id":1,"title":"Task"} {}`,
		`{"team_id":0,"title":"Task"}`,
		`{"team_id":1,"title":""}`,
		`{"team_id":1,"title":"  "}`,
		`{"team_id":1,"title":"Task","assignee_id":0}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", strings.NewReader(body))
		req = req.WithContext(contextWithUserID(req, 42))
		rec := httptest.NewRecorder()
		NewHandler(nil, nil, testSecret).CreateTask(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %q: status = %d, want 400", body, rec.Code)
		}
	}
}

func TestListTasksValidation(t *testing.T) {
	for _, query := range []string{
		"", "?team_id=0", "?team_id=bad", "?team_id=1&status=bad",
		"?team_id=1&assignee_id=0", "?team_id=1&assignee_id=bad",
		"?team_id=1&limit=0", "?team_id=1&limit=101", "?team_id=1&limit=bad",
		"?team_id=1&offset=-1", "?team_id=1&offset=bad",
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks"+query, nil)
		req = req.WithContext(contextWithUserID(req, 42))
		rec := httptest.NewRecorder()
		NewHandler(nil, nil, testSecret).ListTasks(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("query %q: status = %d, want 400", query, rec.Code)
		}
	}
}
