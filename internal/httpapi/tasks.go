package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

type createTaskRequest struct {
	TeamID      uint64  `json:"team_id"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	AssigneeID  *uint64 `json:"assignee_id"`
}

type taskResponse struct {
	ID          uint64     `json:"id"`
	TeamID      uint64     `json:"team_id"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Status      string     `json:"status"`
	CreatedBy   uint64     `json:"created_by"`
	AssigneeID  *uint64    `json:"assignee_id"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	ClosedAt    *time.Time `json:"closed_at"`
	Version     int        `json:"version"`
}

type taskListFilter struct {
	teamID     uint64
	status     string
	assigneeID uint64
	limit      uint64
	offset     uint64
}

// CreateTask создаёт задачу и первую запись её истории.
func (h *Handler) CreateTask(w http.ResponseWriter, r *http.Request) {
	var input createTaskRequest
	if !decodeTeamJSON(w, r, &input) {
		return
	}
	if input.TeamID == 0 || strings.TrimSpace(input.Title) == "" || input.AssigneeID != nil && *input.AssigneeID == 0 {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	userID, ok := UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer func() { _ = tx.Rollback() }()
	var one int
	err = tx.QueryRowContext(r.Context(), "SELECT 1 FROM team_members WHERE team_id = ? AND user_id = ?", input.TeamID, userID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "team not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if input.AssigneeID != nil {
		err = tx.QueryRowContext(r.Context(), "SELECT 1 FROM team_members WHERE team_id = ? AND user_id = ?", input.TeamID, *input.AssigneeID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusBadRequest, "assignee must be a team member")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
	}
	result, err := tx.ExecContext(r.Context(), "INSERT INTO tasks (team_id, title, description, created_by, assignee_id) VALUES (?, ?, ?, ?, ?)", input.TeamID, input.Title, input.Description, userID, input.AssigneeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	id, err := result.LastInsertId()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	response := taskResponse{ID: uint64(id), TeamID: input.TeamID, Title: input.Title, Description: input.Description, Status: "todo", CreatedBy: userID, AssigneeID: input.AssigneeID, Version: 1}
	changes, err := json.Marshal(map[string]any{"created": map[string]any{
		"title":       input.Title,
		"description": input.Description,
		"status":      "todo",
		"assignee_id": input.AssigneeID,
	}})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err = tx.QueryRowContext(r.Context(), "SELECT created_at, updated_at FROM tasks WHERE id = ?", response.ID).Scan(&response.CreatedAt, &response.UpdatedAt); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if _, err = tx.ExecContext(r.Context(), "INSERT INTO task_history (task_id, changed_by, changes, created_at) VALUES (?, ?, ?, ?)", response.ID, userID, changes, response.CreatedAt); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err = tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	h.invalidateTaskLists(r, input.TeamID)
	writeJSON(w, http.StatusCreated, response)
}

// ListTasks возвращает задачи команды с фильтрами и пагинацией.
func (h *Handler) ListTasks(w http.ResponseWriter, r *http.Request) {
	filter, ok := parseTaskListFilter(w, r)
	if !ok {
		return
	}
	userID, ok := UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var one int
	err := h.db.QueryRowContext(r.Context(), "SELECT 1 FROM team_members WHERE team_id = ? AND user_id = ?", filter.teamID, userID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "team not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	generation := h.taskCacheGeneration(r.Context(), filter.teamID)
	cacheKey := taskListCacheKey(filter, generation)
	if h.cache != nil {
		cached, err := h.cache.Get(r.Context(), cacheKey).Bytes()
		if err != nil && !errors.Is(err, redis.Nil) {
			log.Printf("read task list cache: %v", err)
		}
		if err == nil {
			var tasks []taskResponse
			if err := json.Unmarshal(cached, &tasks); err != nil {
				log.Printf("decode task list cache: %v", err)
			} else {
				writeJSON(w, http.StatusOK, tasks)
				return
			}
		}
	}
	query := strings.Builder{}
	args := []any{filter.teamID}
	query.WriteString("SELECT id, team_id, title, description, status, created_by, assignee_id, created_at, updated_at, closed_at, version FROM tasks WHERE team_id = ?")
	if filter.status != "" {
		query.WriteString(" AND status = ?")
		args = append(args, filter.status)
	}
	if filter.assigneeID != 0 {
		query.WriteString(" AND assignee_id = ?")
		args = append(args, filter.assigneeID)
	}
	query.WriteString(" ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?")
	args = append(args, filter.limit, filter.offset)
	rows, err := h.db.QueryContext(r.Context(), query.String(), args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer func() { _ = rows.Close() }()
	tasks := make([]taskResponse, 0)
	for rows.Next() {
		var task taskResponse
		if err := rows.Scan(&task.ID, &task.TeamID, &task.Title, &task.Description, &task.Status, &task.CreatedBy, &task.AssigneeID, &task.CreatedAt, &task.UpdatedAt, &task.ClosedAt, &task.Version); err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	h.storeTaskList(r, filter.teamID, generation, cacheKey, tasks)
	writeJSON(w, http.StatusOK, tasks)
}

func taskListCacheKey(filter taskListFilter, generation uint64) string {
	return fmt.Sprintf("tasks:%d:v%d:%s:%d:%d:%d", filter.teamID, generation, filter.status, filter.assigneeID, filter.limit, filter.offset)
}

func (h *Handler) taskCacheGeneration(ctx context.Context, teamID uint64) uint64 {
	if h.cache == nil {
		return 0
	}
	generation, err := h.cache.Get(ctx, fmt.Sprintf("tasks:gen:%d", teamID)).Uint64()
	if err != nil && !errors.Is(err, redis.Nil) {
		log.Printf("read task cache generation: %v", err)
	}
	return generation
}

func (h *Handler) storeTaskList(r *http.Request, teamID, generation uint64, cacheKey string, tasks []taskResponse) {
	if h.cache == nil || h.taskCacheGeneration(r.Context(), teamID) != generation {
		return
	}
	cached, err := json.Marshal(tasks)
	if err != nil {
		return
	}
	if err := h.cache.Set(r.Context(), cacheKey, cached, 5*time.Minute).Err(); err != nil {
		log.Printf("store task list cache: %v", err)
	}
}

func (h *Handler) invalidateTaskLists(r *http.Request, teamID uint64) {
	if h.cache == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Second)
	defer cancel()
	if err := h.cache.Incr(ctx, fmt.Sprintf("tasks:gen:%d", teamID)).Err(); err != nil {
		log.Printf("invalidate task list cache: %v", err)
	}
}

func parseTaskListFilter(w http.ResponseWriter, r *http.Request) (taskListFilter, bool) {
	filter := taskListFilter{limit: 20}
	query := r.URL.Query()
	var err error
	filter.teamID, err = strconv.ParseUint(query.Get("team_id"), 10, 64)
	if err != nil || filter.teamID == 0 {
		writeError(w, http.StatusBadRequest, "invalid request")
		return filter, false
	}
	filter.status = query.Get("status")
	if filter.status != "" && filter.status != "todo" && filter.status != "in_progress" && filter.status != "done" {
		writeError(w, http.StatusBadRequest, "invalid request")
		return filter, false
	}
	for name, target := range map[string]*uint64{"assignee_id": &filter.assigneeID, "limit": &filter.limit, "offset": &filter.offset} {
		value := query.Get(name)
		if value == "" {
			continue
		}
		*target, err = strconv.ParseUint(value, 10, 64)
		if err != nil || name != "offset" && *target == 0 || name == "limit" && *target > 100 {
			writeError(w, http.StatusBadRequest, "invalid request")
			return filter, false
		}
	}
	return filter, true
}
