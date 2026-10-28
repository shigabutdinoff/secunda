package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
)

type taskHistoryResponse struct {
	ID        uint64          `json:"id"`
	TaskID    uint64          `json:"task_id"`
	ChangedBy uint64          `json:"changed_by"`
	Changes   json.RawMessage `json:"changes" swaggertype:"object"`
	CreatedAt time.Time       `json:"created_at"`
}

type taskCommentResponse struct {
	ID        uint64    `json:"id"`
	TaskID    uint64    `json:"task_id"`
	UserID    uint64    `json:"user_id"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

type createTaskCommentRequest struct {
	Content string `json:"content"`
}

func (h *Handler) accessibleTask(w http.ResponseWriter, r *http.Request) (uint64, bool) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id == 0 {
		writeError(w, http.StatusBadRequest, "invalid request")
		return 0, false
	}
	userID, ok := UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return 0, false
	}
	var one int
	err = h.db.QueryRowContext(r.Context(), `SELECT 1 FROM tasks t
		JOIN team_members tm ON tm.team_id = t.team_id AND tm.user_id = ? WHERE t.id = ?`, userID, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "task not found")
		return 0, false
	}
	if writeTaskInternalError(w, err) {
		return 0, false
	}
	return id, true
}

// ListTaskHistory возвращает историю задачи участнику её команды.
// @Summary История изменений задачи
// @Description changes при создании — {"created":{"title":"a",...}}, при изменении — поля со старым и новым значением: {"title":{"old":"a","new":"b"}}.
// @Tags tasks
// @Produce json
// @Param id path int true "Задача"
// @Success 200 {array} taskHistoryResponse
// @Failure 400 {object} object{error=string} "invalid request"
// @Failure 401 {object} object{error=string} "unauthorized"
// @Failure 404 {object} object{error=string} "task not found"
// @Security Bearer
// @Router /api/v1/tasks/{id}/history [get]
func (h *Handler) ListTaskHistory(w http.ResponseWriter, r *http.Request) {
	id, ok := h.accessibleTask(w, r)
	if !ok {
		return
	}
	rows, err := h.db.QueryContext(r.Context(), "SELECT id, task_id, changed_by, changes, created_at FROM task_history WHERE task_id = ? ORDER BY created_at, id", id)
	if writeTaskInternalError(w, err) {
		return
	}
	defer func() { _ = rows.Close() }()
	history := make([]taskHistoryResponse, 0)
	for rows.Next() {
		var entry taskHistoryResponse
		if err := rows.Scan(&entry.ID, &entry.TaskID, &entry.ChangedBy, &entry.Changes, &entry.CreatedAt); writeTaskInternalError(w, err) {
			return
		}
		history = append(history, entry)
	}
	if writeTaskInternalError(w, rows.Err()) {
		return
	}
	writeJSON(w, http.StatusOK, history)
}

// CreateTaskComment добавляет комментарий от участника команды задачи.
// @Summary Комментарий к задаче
// @Tags tasks
// @Accept json
// @Produce json
// @Param id path int true "Задача"
// @Param body body createTaskCommentRequest true "Текст до 5000 символов"
// @Success 201 {object} taskCommentResponse
// @Failure 400 {object} object{error=string} "invalid request"
// @Failure 401 {object} object{error=string} "unauthorized"
// @Failure 404 {object} object{error=string} "task not found"
// @Security Bearer
// @Router /api/v1/tasks/{id}/comments [post]
func (h *Handler) CreateTaskComment(w http.ResponseWriter, r *http.Request) {
	id, ok := h.accessibleTask(w, r)
	if !ok {
		return
	}
	var input createTaskCommentRequest
	if !decodeTeamJSON(w, r, &input) {
		return
	}
	if strings.TrimSpace(input.Content) == "" || utf8.RuneCountInString(input.Content) > 5000 {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	userID, _ := UserID(r.Context())
	result, err := h.db.ExecContext(r.Context(), "INSERT INTO task_comments (task_id, user_id, content) VALUES (?, ?, ?)", id, userID, input.Content)
	if writeTaskInternalError(w, err) {
		return
	}
	commentID, err := result.LastInsertId()
	if writeTaskInternalError(w, err) {
		return
	}
	comment := taskCommentResponse{ID: uint64(commentID), TaskID: id, UserID: userID, Content: input.Content}
	err = h.db.QueryRowContext(r.Context(), "SELECT created_at FROM task_comments WHERE id = ?", comment.ID).Scan(&comment.CreatedAt)
	if writeTaskInternalError(w, err) {
		return
	}
	writeJSON(w, http.StatusCreated, comment)
}

// ListTaskComments возвращает комментарии задачи участнику её команды.
// @Summary Комментарии задачи
// @Tags tasks
// @Produce json
// @Param id path int true "Задача"
// @Success 200 {array} taskCommentResponse
// @Failure 400 {object} object{error=string} "invalid request"
// @Failure 401 {object} object{error=string} "unauthorized"
// @Failure 404 {object} object{error=string} "task not found"
// @Security Bearer
// @Router /api/v1/tasks/{id}/comments [get]
func (h *Handler) ListTaskComments(w http.ResponseWriter, r *http.Request) {
	id, ok := h.accessibleTask(w, r)
	if !ok {
		return
	}
	rows, err := h.db.QueryContext(r.Context(), "SELECT id, task_id, user_id, content, created_at FROM task_comments WHERE task_id = ? ORDER BY created_at, id", id)
	if writeTaskInternalError(w, err) {
		return
	}
	defer func() { _ = rows.Close() }()
	comments := make([]taskCommentResponse, 0)
	for rows.Next() {
		var comment taskCommentResponse
		if err := rows.Scan(&comment.ID, &comment.TaskID, &comment.UserID, &comment.Content, &comment.CreatedAt); writeTaskInternalError(w, err) {
			return
		}
		comments = append(comments, comment)
	}
	if writeTaskInternalError(w, rows.Err()) {
		return
	}
	writeJSON(w, http.StatusOK, comments)
}
