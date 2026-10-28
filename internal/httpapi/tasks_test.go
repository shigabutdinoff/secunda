package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
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

func TestUpdateTaskValidation(t *testing.T) {
	cases := []struct{ id, body string }{
		{"1", `{"version":`},
		{"1", `{"version":1} {}`},
		{"bad", `{"version":1}`},
		{"0", `{"version":1}`},
		{"1", `{}`},
		{"1", `{"version":0}`},
		{"1", `{"version":-1}`},
		{"1", `{"version":1,"title":"  "}`},
		{"1", `{"version":1,"status":"bad"}`},
		{"1", `{"version":1,"assignee_id":0}`},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodPut, "/api/v1/tasks/"+tc.id, strings.NewReader(tc.body))
		routeCtx := chi.NewRouteContext()
		routeCtx.URLParams.Add("id", tc.id)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
		req = req.WithContext(contextWithUserID(req, 42))
		rec := httptest.NewRecorder()
		NewHandler(nil, nil, testSecret).UpdateTask(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("id %q, body %q: status = %d, want 400", tc.id, tc.body, rec.Code)
		}
	}
}

func TestUpdateTaskAssigneeDecoding(t *testing.T) {
	var omitted updateTaskRequest
	if err := json.Unmarshal([]byte(`{"version":1}`), &omitted); err != nil {
		t.Fatal(err)
	}
	if omitted.AssigneeID.set || omitted.AssigneeID.value != nil {
		t.Fatalf("omitted assignee = %+v, want unset", omitted.AssigneeID)
	}
	var cleared updateTaskRequest
	if err := json.Unmarshal([]byte(`{"version":1,"assignee_id":null}`), &cleared); err != nil {
		t.Fatal(err)
	}
	if !cleared.AssigneeID.set || cleared.AssigneeID.value != nil {
		t.Fatalf("null assignee = %+v, want explicit clear", cleared.AssigneeID)
	}
	var assigned updateTaskRequest
	if err := json.Unmarshal([]byte(`{"version":1,"assignee_id":7}`), &assigned); err != nil {
		t.Fatal(err)
	}
	if !assigned.AssigneeID.set || assigned.AssigneeID.value == nil || *assigned.AssigneeID.value != 7 {
		t.Fatalf("numeric assignee = %+v, want explicit 7", assigned.AssigneeID)
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
