package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/shigabutdinoff/secunda/internal/domain"
)

type nullableUint64 struct {
	set   bool
	value *uint64
}

func (n *nullableUint64) UnmarshalJSON(data []byte) error {
	n.set = true
	if string(data) == "null" {
		n.value = nil
		return nil
	}
	var value uint64
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	n.value = &value
	return nil
}

type updateTaskRequest struct {
	Version     *int           `json:"version" binding:"required"`
	Title       *string        `json:"title"`
	Description *string        `json:"description"`
	Status      *string        `json:"status"`
	AssigneeID  nullableUint64 `json:"assignee_id" swaggertype:"integer"`
}

func writeTaskInternalError(w http.ResponseWriter, r *http.Request, err error) bool {
	if err != nil {
		internalError(w, r, err)
	}
	return err != nil
}

// UpdateTask частично обновляет задачу и атомарно сохраняет историю изменений.
// @Summary Частичное обновление задачи
// @Description Меняет только переданные поля, version — текущая версия задачи. Изменения пишутся в историю.
// @Tags tasks
// @Accept json
// @Produce json
// @Param id path int true "Задача"
// @Param body body updateTaskRequest true "Версия и изменяемые поля, assignee_id null снимает исполнителя"
// @Success 200 {object} taskResponse
// @Failure 400 {object} object{error=string} "invalid request или assignee must be a team member"
// @Failure 401 {object} object{error=string} "unauthorized"
// @Failure 403 {object} object{error=string} "forbidden"
// @Failure 404 {object} object{error=string} "task not found"
// @Failure 409 {object} object{error=string} "version conflict"
// @Security Bearer
// @Router /api/v1/tasks/{id} [put]
func (h *Handler) UpdateTask(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 64)
	var input updateTaskRequest
	if err != nil || id == 0 || !decodeTeamJSON(w, r, &input) {
		if err != nil || id == 0 {
			writeError(w, http.StatusBadRequest, "invalid request")
		}
		return
	}
	if input.Version == nil || *input.Version <= 0 || input.Title != nil && strings.TrimSpace(*input.Title) == "" ||
		input.Status != nil && *input.Status != "todo" && *input.Status != "in_progress" && *input.Status != "done" ||
		input.AssigneeID.set && input.AssigneeID.value != nil && *input.AssigneeID.value == 0 {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	userID, ok := UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if writeTaskInternalError(w, r, err) {
		return
	}
	defer func() { _ = tx.Rollback() }()
	task, role := taskResponse{}, ""
	err = tx.QueryRowContext(r.Context(), `SELECT t.id, t.team_id, t.title, t.description, t.status, t.created_by, t.assignee_id, t.created_at, t.updated_at, t.closed_at, t.version, tm.role
		FROM tasks t JOIN team_members tm ON tm.team_id = t.team_id AND tm.user_id = ? WHERE t.id = ? FOR UPDATE`, userID, id).
		Scan(&task.ID, &task.TeamID, &task.Title, &task.Description, &task.Status, &task.CreatedBy, &task.AssigneeID, &task.CreatedAt, &task.UpdatedAt, &task.ClosedAt, &task.Version, &role)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	if writeTaskInternalError(w, r, err) {
		return
	}
	patch := domain.Patch{Title: input.Title != nil, Description: input.Description != nil, Status: input.Status != nil, AssigneeID: input.AssigneeID.set}
	if !domain.CanEdit(role, domain.Task{CreatedBy: task.CreatedBy, AssigneeID: task.AssigneeID}, userID, patch) {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	if task.Version != *input.Version {
		writeError(w, http.StatusConflict, "version conflict")
		return
	}
	changes := map[string]any{}
	sets, args := []string{}, []any{}
	if input.Title != nil && *input.Title != task.Title {
		changes["title"] = map[string]any{"old": task.Title, "new": *input.Title}
		sets = append(sets, "title = ?")
		args = append(args, *input.Title)
	}
	if input.Description != nil && *input.Description != task.Description {
		changes["description"] = map[string]any{"old": task.Description, "new": *input.Description}
		sets = append(sets, "description = ?")
		args = append(args, *input.Description)
	}
	if input.Status != nil && *input.Status != task.Status {
		changes["status"] = map[string]any{"old": task.Status, "new": *input.Status}
		sets = append(sets, "status = ?")
		args = append(args, *input.Status)
		if *input.Status == "done" {
			sets = append(sets, "closed_at = NOW()")
		} else if task.Status == "done" {
			sets = append(sets, "closed_at = NULL")
		}
	}
	assigneeChanged := input.AssigneeID.set && ((task.AssigneeID == nil) != (input.AssigneeID.value == nil) ||
		task.AssigneeID != nil && input.AssigneeID.value != nil && *task.AssigneeID != *input.AssigneeID.value)
	if assigneeChanged {
		if input.AssigneeID.value != nil {
			var one int
			err = tx.QueryRowContext(r.Context(), "SELECT 1 FROM team_members WHERE team_id = ? AND user_id = ?", task.TeamID, *input.AssigneeID.value).Scan(&one)
			if errors.Is(err, sql.ErrNoRows) {
				writeError(w, http.StatusBadRequest, "assignee must be a team member")
				return
			}
			if writeTaskInternalError(w, r, err) {
				return
			}
		}
		changes["assignee_id"] = map[string]any{"old": task.AssigneeID, "new": input.AssigneeID.value}
		sets = append(sets, "assignee_id = ?")
		args = append(args, input.AssigneeID.value)
	}
	if len(changes) == 0 {
		if writeTaskInternalError(w, r, tx.Commit()) {
			return
		}
		writeJSON(w, http.StatusOK, task)
		return
	}
	args = append(args, id, *input.Version)
	result, err := tx.ExecContext(r.Context(), "UPDATE tasks SET "+strings.Join(sets, ", ")+", version = version + 1, updated_at = NOW() WHERE id = ? AND version = ?", args...)
	if writeTaskInternalError(w, r, err) {
		return
	}
	affected, err := result.RowsAffected()
	if writeTaskInternalError(w, r, err) {
		return
	}
	if affected != 1 {
		writeError(w, http.StatusConflict, "version conflict")
		return
	}
	history, err := json.Marshal(changes)
	if writeTaskInternalError(w, r, err) {
		return
	}
	if _, err = tx.ExecContext(r.Context(), "INSERT INTO task_history (task_id, changed_by, changes) VALUES (?, ?, ?)", id, userID, history); writeTaskInternalError(w, r, err) {
		return
	}
	err = tx.QueryRowContext(r.Context(), "SELECT id, team_id, title, description, status, created_by, assignee_id, created_at, updated_at, closed_at, version FROM tasks WHERE id = ?", id).
		Scan(&task.ID, &task.TeamID, &task.Title, &task.Description, &task.Status, &task.CreatedBy, &task.AssigneeID, &task.CreatedAt, &task.UpdatedAt, &task.ClosedAt, &task.Version)
	if writeTaskInternalError(w, r, err) {
		return
	}
	if writeTaskInternalError(w, r, tx.Commit()) {
		return
	}
	h.invalidateTaskLists(r, task.TeamID)
	writeJSON(w, http.StatusOK, task)
}
