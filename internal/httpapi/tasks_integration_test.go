package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-sql-driver/mysql"
	"github.com/redis/go-redis/v9"
)

type taskTestFixture struct {
	db       *sql.DB
	handler  *Handler
	router   http.Handler
	teamID   uint64
	ownerID  uint64
	adminID  uint64
	memberID uint64
	otherID  uint64
}

func newTaskTestFixture(t *testing.T, cache *redis.Client) taskTestFixture {
	t.Helper()
	db := openIntegrationTestDB(t)
	h := NewHandler(db, cache, testSecret)
	r := chi.NewRouter()
	r.Route("/api/v1/tasks", func(r chi.Router) {
		r.Use(h.Authenticate)
		r.Post("/", h.CreateTask)
		r.Get("/", h.ListTasks)
		r.Put("/{id}", h.UpdateTask)
	})
	owner := createTeamTestUser(t, db, "task-owner")
	admin := createTeamTestUser(t, db, "task-admin")
	member := createTeamTestUser(t, db, "task-member")
	other := createTeamTestUser(t, db, "task-outsider")
	result, err := db.ExecContext(t.Context(), "INSERT INTO teams (name, created_by) VALUES (?, ?)", fmt.Sprintf("Tasks %d", time.Now().UnixNano()), owner)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	teamID := uint64(id)
	for _, row := range []struct {
		userID uint64
		role   string
	}{{owner, "owner"}, {admin, "admin"}, {member, "member"}} {
		if _, err := db.ExecContext(t.Context(), "INSERT INTO team_members (team_id, user_id, role) VALUES (?, ?, ?)", teamID, row.userID, row.role); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(t.Context(), "DELETE th FROM task_history th JOIN tasks t ON t.id = th.task_id WHERE t.team_id = ?", teamID)
		_, _ = db.ExecContext(t.Context(), "DELETE FROM tasks WHERE team_id = ?", teamID)
		_, _ = db.ExecContext(t.Context(), "DELETE FROM team_members WHERE team_id = ?", teamID)
		_, _ = db.ExecContext(t.Context(), "DELETE FROM teams WHERE id = ?", teamID)
		_, _ = db.ExecContext(t.Context(), "DELETE FROM users WHERE id IN (?, ?, ?, ?)", owner, admin, member, other)
	})
	return taskTestFixture{db: db, handler: h, router: r, teamID: teamID, ownerID: owner, adminID: admin, memberID: member, otherID: other}
}

func (f taskTestFixture) request(t *testing.T, userID uint64, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return teamAPIRequest(t, f.handler, f.router, userID, method, path, body)
}

func TestCreateTaskIntegration(t *testing.T) {
	f := newTaskTestFixture(t, nil)
	for _, creator := range []uint64{f.ownerID, f.adminID, f.memberID} {
		body := fmt.Sprintf(`{"team_id":%d,"title":"Created task","description":"Details","assignee_id":%d}`, f.teamID, f.memberID)
		rec := f.request(t, creator, http.MethodPost, "/api/v1/tasks", body)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create by %d = (%d, %s); want 201", creator, rec.Code, rec.Body)
		}
		var task struct {
			ID          uint64  `json:"id"`
			TeamID      uint64  `json:"team_id"`
			Status      string  `json:"status"`
			CreatedBy   uint64  `json:"created_by"`
			AssigneeID  *uint64 `json:"assignee_id"`
			Version     int     `json:"version"`
			Title       string  `json:"title"`
			Description string  `json:"description"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &task); err != nil {
			t.Fatal(err)
		}
		if task.ID == 0 || task.TeamID != f.teamID || task.Status != "todo" || task.CreatedBy != creator || task.AssigneeID == nil || *task.AssigneeID != f.memberID || task.Version != 1 || task.Title != "Created task" || task.Description != "Details" {
			t.Fatalf("unexpected task response: %s", rec.Body)
		}
		var changedBy uint64
		var changes []byte
		if err := f.db.QueryRowContext(t.Context(), "SELECT changed_by, changes FROM task_history WHERE task_id = ?", task.ID).Scan(&changedBy, &changes); err != nil {
			t.Fatal(err)
		}
		var history struct {
			Created map[string]any `json:"created"`
		}
		if err := json.Unmarshal(changes, &history); err != nil {
			t.Fatal(err)
		}
		if changedBy != creator || history.Created["title"] != "Created task" || history.Created["status"] != "todo" {
			t.Fatalf("history = (%d, %s)", changedBy, changes)
		}
	}
}

func TestCreateTaskAccessIntegration(t *testing.T) {
	f := newTaskTestFixture(t, nil)
	body := fmt.Sprintf(`{"team_id":%d,"title":"Task","description":""}`, f.teamID)
	if rec := f.request(t, f.otherID, http.MethodPost, "/api/v1/tasks", body); rec.Code != http.StatusNotFound {
		t.Fatalf("outsider = (%d, %s); want 404", rec.Code, rec.Body)
	}
	body = fmt.Sprintf(`{"team_id":%d,"title":"Task","description":"","assignee_id":%d}`, f.teamID, f.otherID)
	if rec := f.request(t, f.ownerID, http.MethodPost, "/api/v1/tasks", body); rec.Code != http.StatusBadRequest {
		t.Fatalf("outside assignee = (%d, %s); want 400", rec.Code, rec.Body)
	}
}

func TestCreateTaskRollbackIntegration(t *testing.T) {
	f := newTaskTestFixture(t, nil)
	trigger := fmt.Sprintf("reject_task_history_%d", time.Now().UnixNano())
	if _, err := f.db.ExecContext(t.Context(), fmt.Sprintf("CREATE TRIGGER %s BEFORE INSERT ON task_history FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'forced failure'", trigger)); err != nil {
		if mysqlErr, ok := err.(*mysql.MySQLError); ok && mysqlErr.Number == 1419 {
			t.Skip("создание trigger запрещено настройками MySQL")
		}
		t.Fatal(err)
	}
	defer func() { _, _ = f.db.ExecContext(t.Context(), "DROP TRIGGER "+trigger) }()
	body := fmt.Sprintf(`{"team_id":%d,"title":"Rollback task","description":""}`, f.teamID)
	if rec := f.request(t, f.ownerID, http.MethodPost, "/api/v1/tasks", body); rec.Code != http.StatusInternalServerError {
		t.Fatalf("create = (%d, %s); want 500", rec.Code, rec.Body)
	}
	var count int
	if err := f.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM tasks WHERE team_id = ? AND title = 'Rollback task'", f.teamID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("tasks after rollback = %d, error = %v", count, err)
	}
}

func createTaskForTest(t *testing.T, f taskTestFixture, title string, assigneeID uint64) taskResponse {
	t.Helper()
	body := fmt.Sprintf(`{"team_id":%d,"title":%q,"description":"","assignee_id":%d}`, f.teamID, title, assigneeID)
	rec := f.request(t, f.ownerID, http.MethodPost, "/api/v1/tasks", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %q = (%d, %s)", title, rec.Code, rec.Body)
	}
	var task taskResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	return task
}

func listTasksForTest(t *testing.T, f taskTestFixture, userID uint64, query string) *httptest.ResponseRecorder {
	t.Helper()
	return f.request(t, userID, http.MethodGet, fmt.Sprintf("/api/v1/tasks?team_id=%d%s", f.teamID, query), "")
}

func TestListTasksIntegration(t *testing.T) {
	f := newTaskTestFixture(t, nil)
	first := createTaskForTest(t, f, "First", f.memberID)
	second := createTaskForTest(t, f, "Second", f.adminID)
	third := createTaskForTest(t, f, "Third", f.memberID)
	if _, err := f.db.ExecContext(t.Context(), "UPDATE tasks SET status = 'done', closed_at = NOW() WHERE id = ?", third.ID); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		query string
		ids   []uint64
	}{
		{"", []uint64{third.ID, second.ID, first.ID}},
		{"&status=done", []uint64{third.ID}},
		{"&assignee_id=" + fmt.Sprint(f.memberID), []uint64{third.ID, first.ID}},
		{"&status=done&assignee_id=" + fmt.Sprint(f.memberID), []uint64{third.ID}},
		{"&limit=1&offset=1", []uint64{second.ID}},
	} {
		rec := listTasksForTest(t, f, f.memberID, tc.query)
		if rec.Code != http.StatusOK {
			t.Fatalf("list %q = (%d, %s)", tc.query, rec.Code, rec.Body)
		}
		var tasks []taskResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &tasks); err != nil {
			t.Fatal(err)
		}
		if len(tasks) != len(tc.ids) {
			t.Fatalf("list %q = %s; want ids %v", tc.query, rec.Body, tc.ids)
		}
		for i, id := range tc.ids {
			if tasks[i].ID != id {
				t.Fatalf("list %q = %s; want ids %v", tc.query, rec.Body, tc.ids)
			}
		}
	}
	if rec := listTasksForTest(t, f, f.otherID, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("outsider = (%d, %s); want 404", rec.Code, rec.Body)
	}
}

func TestTaskListCacheIntegration(t *testing.T) {
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("TEST_REDIS_ADDR не задан")
	}
	cache := redis.NewClient(&redis.Options{Addr: addr, DB: 15})
	defer func() { _ = cache.Close() }()
	f := newTaskTestFixture(t, cache)
	t.Cleanup(func() {
		keys, _ := cache.Keys(t.Context(), "tasks:*:"+fmt.Sprint(f.teamID)+"*").Result()
		if len(keys) > 0 {
			_ = cache.Del(t.Context(), keys...).Err()
		}
	})
	first := createTaskForTest(t, f, "Cached", f.memberID)
	if rec := listTasksForTest(t, f, f.memberID, ""); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	if _, err := f.db.ExecContext(t.Context(), "INSERT INTO tasks (team_id, title, description, created_by) VALUES (?, 'SQL only', '', ?)", f.teamID, f.ownerID); err != nil {
		t.Fatal(err)
	}
	rec := listTasksForTest(t, f, f.memberID, "")
	var cached []taskResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &cached); err != nil || len(cached) != 1 || cached[0].ID != first.ID {
		t.Fatalf("cached list = %s, error = %v", rec.Body, err)
	}
	keys, err := cache.Keys(t.Context(), "tasks:list:"+fmt.Sprint(f.teamID)+":*").Result()
	if err != nil || len(keys) != 1 {
		t.Fatalf("cache keys = %v, error = %v", keys, err)
	}
	ttl, err := cache.TTL(t.Context(), keys[0]).Result()
	if err != nil || ttl <= 0 || ttl > 5*time.Minute {
		t.Fatalf("cache TTL = %v, error = %v", ttl, err)
	}
	second := createTaskForTest(t, f, "After invalidation", f.memberID)
	staleRequest := httptest.NewRequest(http.MethodGet, "/api/v1/tasks", nil)
	f.handler.storeTaskList(staleRequest, f.teamID, 0, fmt.Sprintf("tasks:list:%d:0::0:20:0", f.teamID), cached)
	rec = listTasksForTest(t, f, f.memberID, "")
	var refreshed []taskResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &refreshed); err != nil || len(refreshed) != 3 || refreshed[2].ID != second.ID {
		t.Fatalf("refreshed list = %s, error = %v", rec.Body, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	f.handler.invalidateTaskLists(httptest.NewRequest(http.MethodPost, "/api/v1/tasks", nil).WithContext(ctx), f.teamID)
	generation, err := cache.Get(t.Context(), "tasks:generation:"+fmt.Sprint(f.teamID)).Uint64()
	if err != nil || generation != 3 {
		t.Fatalf("generation after canceled invalidation = %d, error = %v; want 3", generation, err)
	}
}

func TestTasksWithUnavailableRedisIntegration(t *testing.T) {
	cache := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 20 * time.Millisecond, MaxRetries: -1})
	defer func() { _ = cache.Close() }()
	f := newTaskTestFixture(t, cache)
	createTaskForTest(t, f, "No Redis", f.memberID)
	if rec := listTasksForTest(t, f, f.memberID, ""); rec.Code != http.StatusOK {
		t.Fatalf("list without Redis = (%d, %s)", rec.Code, rec.Body)
	}
}

func updateTaskForTest(t *testing.T, f taskTestFixture, userID, taskID uint64, body string) *httptest.ResponseRecorder {
	t.Helper()
	return f.request(t, userID, http.MethodPut, fmt.Sprintf("/api/v1/tasks/%d", taskID), body)
}

func TestUpdateTaskIntegration(t *testing.T) {
	f := newTaskTestFixture(t, nil)
	task := createTaskForTest(t, f, "Before", f.memberID)
	body := fmt.Sprintf(`{"version":1,"title":"After","description":"Updated","status":"in_progress","assignee_id":%d}`, f.adminID)
	rec := updateTaskForTest(t, f, f.ownerID, task.ID, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("update = (%d, %s); want 200", rec.Code, rec.Body)
	}
	var updated taskResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Title != "After" || updated.Description != "Updated" || updated.Status != "in_progress" || updated.AssigneeID == nil || *updated.AssigneeID != f.adminID || updated.Version != 2 || updated.ClosedAt != nil {
		t.Fatalf("updated task = %s", rec.Body)
	}
	var changedBy uint64
	var changes string
	if err := f.db.QueryRowContext(t.Context(), "SELECT changed_by, changes FROM task_history WHERE task_id = ? ORDER BY id DESC LIMIT 1", task.ID).Scan(&changedBy, &changes); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf(`{"assignee_id":{"old":%d,"new":%d},"description":{"old":"","new":"Updated"},"status":{"old":"todo","new":"in_progress"},"title":{"old":"Before","new":"After"}}`, f.memberID, f.adminID)
	var gotJSON, wantJSON any
	if err := json.Unmarshal([]byte(changes), &gotJSON); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &wantJSON); err != nil {
		t.Fatal(err)
	}
	if changedBy != f.ownerID || !reflect.DeepEqual(gotJSON, wantJSON) {
		t.Fatalf("history = (%d, %s); want (%d, %s)", changedBy, changes, f.ownerID, want)
	}
}

func TestUpdateTaskAccessIntegration(t *testing.T) {
	f := newTaskTestFixture(t, nil)
	for _, tc := range []struct {
		name     string
		caller   uint64
		creator  bool
		assignee bool
		body     string
		want     int
	}{
		{name: "admin", caller: f.adminID, body: `{"version":1,"description":"Admin"}`, want: http.StatusOK},
		{name: "member creator", caller: f.memberID, creator: true, body: `{"version":1,"title":"Creator"}`, want: http.StatusOK},
		{name: "member assignee status", caller: f.memberID, assignee: true, body: `{"version":1,"status":"done"}`, want: http.StatusOK},
		{name: "member assignee title", caller: f.memberID, assignee: true, body: `{"version":1,"title":"Assignee"}`, want: http.StatusForbidden},
		{name: "member other", caller: f.memberID, body: `{"version":1,"description":"Forbidden"}`, want: http.StatusForbidden},
		{name: "member creator status", caller: f.memberID, creator: true, body: `{"version":1,"status":"done"}`, want: http.StatusOK},
		{name: "outsider", caller: f.otherID, body: `{"version":1,"title":"Missing"}`, want: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task := createTaskForTest(t, f, tc.name, f.adminID)
			if tc.creator {
				if _, err := f.db.ExecContext(t.Context(), "UPDATE tasks SET created_by = ? WHERE id = ?", f.memberID, task.ID); err != nil {
					t.Fatal(err)
				}
			}
			if tc.assignee {
				if _, err := f.db.ExecContext(t.Context(), "UPDATE tasks SET assignee_id = ? WHERE id = ?", f.memberID, task.ID); err != nil {
					t.Fatal(err)
				}
			}
			if rec := updateTaskForTest(t, f, tc.caller, task.ID, tc.body); rec.Code != tc.want {
				t.Fatalf("update = (%d, %s); want %d", rec.Code, rec.Body, tc.want)
			}
		})
	}
}

func TestUpdateTaskValidationAndConflictIntegration(t *testing.T) {
	f := newTaskTestFixture(t, nil)
	outside := createTaskForTest(t, f, "Outside assignee", f.memberID)
	body := fmt.Sprintf(`{"version":1,"assignee_id":%d}`, f.otherID)
	if rec := updateTaskForTest(t, f, f.ownerID, outside.ID, body); rec.Code != http.StatusBadRequest {
		t.Fatalf("outside assignee = (%d, %s); want 400", rec.Code, rec.Body)
	}
	stale := createTaskForTest(t, f, "Stable", f.memberID)
	if rec := updateTaskForTest(t, f, f.ownerID, stale.ID, `{"version":0,"title":"Changed"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid version = (%d, %s); want 400", rec.Code, rec.Body)
	}
	if rec := updateTaskForTest(t, f, f.ownerID, stale.ID, `{"version":2,"title":"Changed"}`); rec.Code != http.StatusConflict {
		t.Fatalf("stale update = (%d, %s); want 409", rec.Code, rec.Body)
	}
	var title string
	var version, history int
	if err := f.db.QueryRowContext(t.Context(), "SELECT title, version, (SELECT COUNT(*) FROM task_history WHERE task_id = ?) FROM tasks WHERE id = ?", stale.ID, stale.ID).Scan(&title, &version, &history); err != nil {
		t.Fatal(err)
	}
	if title != "Stable" || version != 1 || history != 1 {
		t.Fatalf("after stale update = (%q, %d, %d); want (Stable, 1, 1)", title, version, history)
	}
}

func TestUpdateTaskSameValueIntegration(t *testing.T) {
	f := newTaskTestFixture(t, nil)
	task := createTaskForTest(t, f, "Same", f.memberID)
	rec := updateTaskForTest(t, f, f.ownerID, task.ID, fmt.Sprintf(`{"version":1,"title":"Same","description":"","status":"todo","assignee_id":%d}`, f.memberID))
	if rec.Code != http.StatusOK {
		t.Fatalf("same-value update = (%d, %s); want 200", rec.Code, rec.Body)
	}
	var updated taskResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil || updated.Version != 1 {
		t.Fatalf("same-value response = %s, error = %v", rec.Body, err)
	}
	var history int
	if err := f.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM task_history WHERE task_id = ?", task.ID).Scan(&history); err != nil || history != 1 {
		t.Fatalf("same-value history = %d, error = %v; want 1", history, err)
	}
}

func TestUpdateTaskClosedAtIntegration(t *testing.T) {
	f := newTaskTestFixture(t, nil)
	task := createTaskForTest(t, f, "Close", f.memberID)
	closed := updateTaskForTest(t, f, f.ownerID, task.ID, `{"version":1,"status":"done"}`)
	var done taskResponse
	if err := json.Unmarshal(closed.Body.Bytes(), &done); err != nil || closed.Code != http.StatusOK || done.ClosedAt == nil {
		t.Fatalf("close = (%d, %s), error = %v", closed.Code, closed.Body, err)
	}
	preserved := updateTaskForTest(t, f, f.ownerID, task.ID, `{"version":2,"title":"Still done"}`)
	var stillDone taskResponse
	if err := json.Unmarshal(preserved.Body.Bytes(), &stillDone); err != nil || preserved.Code != http.StatusOK || stillDone.ClosedAt == nil || !stillDone.ClosedAt.Equal(*done.ClosedAt) {
		t.Fatalf("preserved close = (%d, %s), error = %v; original %v", preserved.Code, preserved.Body, err, done.ClosedAt)
	}
	reopened := updateTaskForTest(t, f, f.ownerID, task.ID, `{"version":3,"status":"todo"}`)
	var todo taskResponse
	if err := json.Unmarshal(reopened.Body.Bytes(), &todo); err != nil || reopened.Code != http.StatusOK || todo.ClosedAt != nil {
		t.Fatalf("reopen = (%d, %s), error = %v", reopened.Code, reopened.Body, err)
	}
}

func TestUpdateTaskRollbackIntegration(t *testing.T) {
	f := newTaskTestFixture(t, nil)
	task := createTaskForTest(t, f, "Before rollback", f.memberID)
	trigger := fmt.Sprintf("reject_task_update_history_%d", time.Now().UnixNano())
	if _, err := f.db.ExecContext(t.Context(), fmt.Sprintf("CREATE TRIGGER %s BEFORE INSERT ON task_history FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'forced failure'", trigger)); err != nil {
		if mysqlErr, ok := err.(*mysql.MySQLError); ok && mysqlErr.Number == 1419 {
			t.Skip("создание trigger запрещено настройками MySQL")
		}
		t.Fatal(err)
	}
	defer func() { _, _ = f.db.ExecContext(t.Context(), "DROP TRIGGER "+trigger) }()
	if rec := updateTaskForTest(t, f, f.ownerID, task.ID, `{"version":1,"title":"After rollback"}`); rec.Code != http.StatusInternalServerError {
		t.Fatalf("update = (%d, %s); want 500", rec.Code, rec.Body)
	}
	var title string
	var version, history int
	if err := f.db.QueryRowContext(t.Context(), "SELECT title, version, (SELECT COUNT(*) FROM task_history WHERE task_id = ?) FROM tasks WHERE id = ?", task.ID, task.ID).Scan(&title, &version, &history); err != nil {
		t.Fatal(err)
	}
	if title != "Before rollback" || version != 1 || history != 1 {
		t.Fatalf("after rollback = (%q, %d, %d); want (Before rollback, 1, 1)", title, version, history)
	}
}

func TestUpdateTaskCacheIntegration(t *testing.T) {
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("TEST_REDIS_ADDR не задан")
	}
	cache := redis.NewClient(&redis.Options{Addr: addr, DB: 15})
	defer func() { _ = cache.Close() }()
	f := newTaskTestFixture(t, cache)
	t.Cleanup(func() {
		keys, _ := cache.Keys(t.Context(), "tasks:*:"+fmt.Sprint(f.teamID)+"*").Result()
		if len(keys) > 0 {
			_ = cache.Del(t.Context(), keys...).Err()
		}
	})
	task := createTaskForTest(t, f, "Cached before", f.memberID)
	if rec := listTasksForTest(t, f, f.memberID, ""); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	if rec := updateTaskForTest(t, f, f.ownerID, task.ID, `{"version":1,"title":"Cached after"}`); rec.Code != http.StatusOK {
		t.Fatalf("update = (%d, %s)", rec.Code, rec.Body)
	}
	rec := listTasksForTest(t, f, f.memberID, "")
	var tasks []taskResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &tasks); err != nil || len(tasks) != 1 || tasks[0].Title != "Cached after" || tasks[0].Version != 2 {
		t.Fatalf("refreshed list = %s, error = %v", rec.Body, err)
	}
}

func TestUpdateTaskConcurrentIntegration(t *testing.T) {
	f := newTaskTestFixture(t, nil)
	task := createTaskForTest(t, f, "Concurrent", f.memberID)
	start := make(chan struct{})
	responses := make([]*httptest.ResponseRecorder, 2)
	var wg sync.WaitGroup
	for i, title := range []string{"First winner", "Second winner"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			responses[i] = updateTaskForTest(t, f, f.ownerID, task.ID, fmt.Sprintf(`{"version":1,"title":%q}`, title))
		}()
	}
	close(start)
	wg.Wait()
	codes := []int{responses[0].Code, responses[1].Code}
	if !((codes[0] == http.StatusOK && codes[1] == http.StatusConflict) || (codes[0] == http.StatusConflict && codes[1] == http.StatusOK)) {
		t.Fatalf("concurrent statuses = %v, bodies = (%s, %s); want one 200 and one 409", codes, responses[0].Body, responses[1].Body)
	}
	winner := 0
	if responses[1].Code == http.StatusOK {
		winner = 1
	}
	wantTitle := "First winner"
	if winner == 1 {
		wantTitle = "Second winner"
	}
	var title string
	var version, history int
	if err := f.db.QueryRowContext(t.Context(), "SELECT title, version, (SELECT COUNT(*) FROM task_history WHERE task_id = ?) FROM tasks WHERE id = ?", task.ID, task.ID).Scan(&title, &version, &history); err != nil {
		t.Fatal(err)
	}
	if title != wantTitle || version != 2 || history != 2 {
		t.Fatalf("after concurrent updates = (%q, %d, %d); want (%q, 2, 2)", title, version, history, wantTitle)
	}
}
