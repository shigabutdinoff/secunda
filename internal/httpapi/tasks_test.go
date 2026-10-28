package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
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
		wantJSON(t, rec)
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
		wantJSON(t, rec)
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

func TestTaskListCacheKey(t *testing.T) {
	base := taskListFilter{teamID: 7, limit: 20}
	for _, tc := range []struct {
		name       string
		filter     taskListFilter
		generation uint64
		want       string
	}{
		{name: "defaults", filter: base, want: "tasks:7:v0::0:20:0:"},
		{name: "team", filter: taskListFilter{teamID: 8, limit: 20}, want: "tasks:8:v0::0:20:0:"},
		{name: "generation", filter: base, generation: 3, want: "tasks:7:v3::0:20:0:"},
		{name: "status", filter: taskListFilter{teamID: 7, status: "done", limit: 20}, want: "tasks:7:v0:done:0:20:0:"},
		{name: "assignee", filter: taskListFilter{teamID: 7, assigneeID: 42, limit: 20}, want: "tasks:7:v0::42:20:0:"},
		{name: "pagination", filter: taskListFilter{teamID: 7, limit: 50, offset: 10}, want: "tasks:7:v0::0:50:10:"},
		{name: "cursor", filter: taskListFilter{teamID: 7, limit: 20, cursor: "YmFkfDE"}, want: "tasks:7:v0::0:20:0:YmFkfDE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := taskListCacheKey(tc.filter, tc.generation); got != tc.want {
				t.Fatalf("key = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTaskCacheLogsRedisErrors(t *testing.T) {
	cache := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 10 * time.Millisecond, MaxRetries: -1})
	defer func() { _ = cache.Close() }()
	core, logs := observer.New(zap.WarnLevel)
	h := NewHandler(nil, cache, testSecret)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks", nil)
	req = req.WithContext(context.WithValue(req.Context(), requestLogKey{}, &requestLog{logger: zap.New(core)}))
	h.taskCacheGeneration(req.Context(), 7)
	h.storeTaskList(req, 7, 0, "tasks:7:v0::0:20:0:", taskListResponse{})
	h.invalidateTaskLists(req, 7)
	for _, operation := range []string{"read task cache generation", "store task list cache", "invalidate task list cache"} {
		if logs.FilterMessage(operation).Len() == 0 {
			t.Errorf("no warning %q in %v", operation, logs.All())
		}
	}
}

func TestListTasksValidation(t *testing.T) {
	for _, query := range []string{
		"", "?team_id=0", "?team_id=bad", "?team_id=1&status=bad",
		"?team_id=1&assignee_id=0", "?team_id=1&assignee_id=bad",
		"?team_id=1&limit=0", "?team_id=1&limit=101", "?team_id=1&limit=bad",
		"?team_id=1&offset=-1", "?team_id=1&offset=bad",
		"?team_id=1&cursor=!!", "?team_id=1&cursor=eA", "?team_id=1&cursor=YmFkfDE",
		"?team_id=1&cursor=MjAyNi0wMS0wMVQwMDowMDowMFp8eA",
		"?team_id=1&offset=0&cursor=MjAyNi0wMS0wMVQwMDowMDowMFp8MQ",
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks"+query, nil)
		req = req.WithContext(contextWithUserID(req, 42))
		rec := httptest.NewRecorder()
		NewHandler(nil, nil, testSecret).ListTasks(rec, req)
		wantJSON(t, rec)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("query %q: status = %d, want 400", query, rec.Code)
		}
	}
}

func TestCreateTaskCommentValidation(t *testing.T) {
	for _, id := range []string{"bad", "0"} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks/"+id+"/comments", strings.NewReader(`{"content":"text"}`))
		routeCtx := chi.NewRouteContext()
		routeCtx.URLParams.Add("id", id)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
		req = req.WithContext(contextWithUserID(req, 42))
		rec := httptest.NewRecorder()
		NewHandler(nil, nil, testSecret).CreateTaskComment(rec, req)
		wantJSON(t, rec)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("id %q: status = %d, want 400", id, rec.Code)
		}
	}
}
