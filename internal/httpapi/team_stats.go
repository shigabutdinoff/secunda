package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

type teamStatsResponse struct {
	ByStatus        map[string]uint64 `json:"by_status"`
	TopAssignees    []topAssignee     `json:"top_assignees"`
	AvgCloseSeconds *float64          `json:"avg_close_seconds"`
	Comments        uint64            `json:"comments"`
}

type topAssignee struct {
	UserID uint64 `json:"user_id"`
	Name   string `json:"name"`
	Closed uint64 `json:"closed"`
}

// GetTeamStats возвращает SQL-отчёт по команде её создателю или администратору.
// @Summary Статистика команды
// @Description Задачи по статусам, три исполнителя с наибольшим числом закрытых за 30 дней задач,
// @Description среднее время закрытия в секундах и число комментариев. Доступно owner и admin.
// @Tags teams
// @Produce json
// @Param team_id path int true "Команда"
// @Success 200 {object} teamStatsResponse
// @Failure 400 {object} object{error=string} "invalid request"
// @Failure 401 {object} object{error=string} "unauthorized"
// @Failure 403 {object} object{error=string} "forbidden"
// @Failure 404 {object} object{error=string} "team not found"
// @Security Bearer
// @Router /api/v1/teams/{team_id}/stats [get]
func (h *Handler) GetTeamStats(w http.ResponseWriter, r *http.Request) {
	teamID, err := strconv.ParseUint(chi.URLParam(r, "team_id"), 10, 64)
	if err != nil || teamID == 0 {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	userID, ok := UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var role string
	err = h.db.QueryRowContext(r.Context(), "SELECT role FROM team_members WHERE team_id = ? AND user_id = ?", teamID, userID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "team not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if role != "owner" && role != "admin" {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	var raw []byte
	err = h.db.QueryRowContext(r.Context(), `WITH team_tasks AS (
		SELECT id, status, assignee_id, created_at, closed_at FROM tasks WHERE team_id = ?
	), top_assignees AS (
		SELECT tt.assignee_id, u.name, COUNT(*) AS closed
		FROM team_tasks tt JOIN users u ON u.id = tt.assignee_id
		WHERE tt.status = 'done' AND tt.closed_at >= NOW() - INTERVAL 30 DAY
		GROUP BY tt.assignee_id, u.name ORDER BY closed DESC, tt.assignee_id ASC LIMIT 3
	)
	SELECT JSON_OBJECT(
		'by_status', (SELECT JSON_OBJECT(
			'todo', COALESCE(SUM(status = 'todo'), 0),
			'in_progress', COALESCE(SUM(status = 'in_progress'), 0),
			'done', COALESCE(SUM(status = 'done'), 0)
		) FROM team_tasks),
		'top_assignees', COALESCE((SELECT JSON_ARRAYAGG(JSON_OBJECT(
			'user_id', assignee_id, 'name', name, 'closed', closed
		)) FROM (SELECT * FROM top_assignees ORDER BY closed DESC, assignee_id ASC) ranked), JSON_ARRAY()),
		'avg_close_seconds', (SELECT AVG(TIMESTAMPDIFF(SECOND, created_at, closed_at)) FROM team_tasks WHERE closed_at IS NOT NULL),
		'comments', (SELECT COUNT(tc.id) FROM team_tasks tt JOIN task_comments tc ON tc.task_id = tt.id)
	)`, teamID).Scan(&raw)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	var response teamStatsResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, response)
}
