package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
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
