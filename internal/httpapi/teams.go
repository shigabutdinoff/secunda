package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-sql-driver/mysql"
)

type createTeamRequest struct {
	Name string `json:"name"`
}

type inviteMemberRequest struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

type teamResponse struct {
	ID        uint64    `json:"id"`
	Name      string    `json:"name"`
	CreatedBy uint64    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

func decodeTeamJSON(w http.ResponseWriter, r *http.Request, input any) bool {
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(input); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request")
		return false
	}
	return true
}

// CreateTeam создаёт команду и назначает текущего пользователя создателем.
// @Summary Создание команды, создатель становится owner
// @Tags teams
// @Accept json
// @Produce json
// @Param body body createTeamRequest true "Название"
// @Success 201 {object} teamResponse
// @Failure 400 {object} object{error=string} "invalid request"
// @Failure 401 {object} object{error=string} "unauthorized"
// @Security Bearer
// @Router /api/v1/teams [post]
func (h *Handler) CreateTeam(w http.ResponseWriter, r *http.Request) {
	var input createTeamRequest
	if !decodeTeamJSON(w, r, &input) {
		return
	}
	userID, ok := UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if strings.TrimSpace(input.Name) == "" {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		internalError(w, r, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(r.Context(), "INSERT INTO teams (name, created_by) VALUES (?, ?)", input.Name, userID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	id, err := result.LastInsertId()
	if err != nil {
		internalError(w, r, err)
		return
	}
	teamID := uint64(id)
	if _, err = tx.ExecContext(r.Context(), "INSERT INTO team_members (team_id, user_id, role) VALUES (?, ?, 'owner')", teamID, userID); err != nil {
		internalError(w, r, err)
		return
	}
	response := teamResponse{ID: teamID, Name: input.Name, CreatedBy: userID}
	if err = tx.QueryRowContext(r.Context(), "SELECT created_at FROM teams WHERE id = ?", teamID).Scan(&response.CreatedAt); err != nil {
		internalError(w, r, err)
		return
	}
	if err = tx.Commit(); err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, response)
}

// ListTeams возвращает команды, в которых состоит текущий пользователь.
// @Summary Команды текущего пользователя
// @Tags teams
// @Produce json
// @Success 200 {array} teamResponse
// @Failure 401 {object} object{error=string} "unauthorized"
// @Security Bearer
// @Router /api/v1/teams [get]
func (h *Handler) ListTeams(w http.ResponseWriter, r *http.Request) {
	userID, ok := UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	rows, err := h.db.QueryContext(r.Context(), `SELECT t.id, t.name, t.created_by, t.created_at
		FROM teams t JOIN team_members tm ON tm.team_id = t.id WHERE tm.user_id = ? ORDER BY t.id`, userID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	defer func() { _ = rows.Close() }()
	teams := make([]teamResponse, 0)
	for rows.Next() {
		var team teamResponse
		if err := rows.Scan(&team.ID, &team.Name, &team.CreatedBy, &team.CreatedAt); err != nil {
			internalError(w, r, err)
			return
		}
		teams = append(teams, team)
	}
	if err := rows.Err(); err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, teams)
}

// InviteMember добавляет пользователя в команду создателем или администратором.
// @Summary Добавление пользователя в команду
// @Description Доступно owner и admin команды.
// @Tags teams
// @Accept json
// @Produce json
// @Param id path int true "Команда"
// @Param body body inviteMemberRequest true "Почта зарегистрированного пользователя и роль"
// @Success 201 {object} object{team_id=int,user_id=int,role=string}
// @Failure 400 {object} object{error=string} "invalid request"
// @Failure 401 {object} object{error=string} "unauthorized"
// @Failure 403 {object} object{error=string} "forbidden"
// @Failure 404 {object} object{error=string} "team not found или user not found"
// @Failure 409 {object} object{error=string} "user already in team"
// @Security Bearer
// @Router /api/v1/teams/{id}/invite [post]
func (h *Handler) InviteMember(w http.ResponseWriter, r *http.Request) {
	teamID, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 64)
	var input inviteMemberRequest
	if err != nil || teamID == 0 || !decodeTeamJSON(w, r, &input) {
		if err != nil || teamID == 0 {
			writeError(w, http.StatusBadRequest, "invalid request")
		}
		return
	}
	if strings.TrimSpace(input.Email) == "" || (input.Role != "admin" && input.Role != "member") {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	userID, ok := UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var callerRole string
	err = h.db.QueryRowContext(r.Context(), "SELECT role FROM team_members WHERE team_id = ? AND user_id = ?", teamID, userID).Scan(&callerRole)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "team not found")
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}
	if callerRole != "owner" && callerRole != "admin" {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	var invitedID uint64
	err = h.db.QueryRowContext(r.Context(), "SELECT id FROM users WHERE email = ?", input.Email).Scan(&invitedID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}
	if _, err = h.db.ExecContext(r.Context(), "INSERT INTO team_members (team_id, user_id, role) VALUES (?, ?, ?)", teamID, invitedID, input.Role); err != nil {
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == mysqlDuplicateEntry {
			writeError(w, http.StatusConflict, "user already in team")
			return
		}
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"team_id": teamID, "user_id": invitedID, "role": input.Role})
}

type updateMemberRequest struct {
	Role string `json:"role"`
}

// teamMemberTarget разбирает {id} и {user_id} и возвращает роли вызывающего и цели в команде.
func (h *Handler) teamMemberTarget(w http.ResponseWriter, r *http.Request) (teamID, targetID uint64, callerRole, targetRole string, ok bool) {
	teamID, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 64)
	if err == nil {
		targetID, err = strconv.ParseUint(chi.URLParam(r, "user_id"), 10, 64)
	}
	if err != nil || teamID == 0 || targetID == 0 {
		writeError(w, http.StatusBadRequest, "invalid request")
		return 0, 0, "", "", false
	}
	userID, ok := UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return 0, 0, "", "", false
	}
	err = h.db.QueryRowContext(r.Context(), "SELECT role FROM team_members WHERE team_id = ? AND user_id = ?", teamID, userID).Scan(&callerRole)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "team not found")
		return 0, 0, "", "", false
	}
	if err == nil {
		err = h.db.QueryRowContext(r.Context(), "SELECT role FROM team_members WHERE team_id = ? AND user_id = ?", teamID, targetID).Scan(&targetRole)
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "member not found")
			return 0, 0, "", "", false
		}
	}
	if err != nil {
		internalError(w, r, err)
		return 0, 0, "", "", false
	}
	return teamID, targetID, callerRole, targetRole, true
}

// UpdateMemberRole меняет роль участника; доступно только создателю, роль создателя не меняется.
// @Summary Смена роли участника
// @Description Доступно только owner. Роль owner не меняется и не выдаётся.
// @Tags teams
// @Accept json
// @Produce json
// @Param id path int true "Команда"
// @Param user_id path int true "Участник"
// @Param body body updateMemberRequest true "Новая роль: admin или member"
// @Success 200 {object} object{team_id=int,user_id=int,role=string}
// @Failure 400 {object} object{error=string} "invalid request"
// @Failure 401 {object} object{error=string} "unauthorized"
// @Failure 403 {object} object{error=string} "forbidden"
// @Failure 404 {object} object{error=string} "team not found или member not found"
// @Security Bearer
// @Router /api/v1/teams/{id}/members/{user_id} [patch]
func (h *Handler) UpdateMemberRole(w http.ResponseWriter, r *http.Request) {
	var input updateMemberRequest
	if !decodeTeamJSON(w, r, &input) {
		return
	}
	if input.Role != "admin" && input.Role != "member" {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	teamID, targetID, callerRole, targetRole, ok := h.teamMemberTarget(w, r)
	if !ok {
		return
	}
	if callerRole != "owner" || targetRole == "owner" {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	res, err := h.db.ExecContext(r.Context(), "UPDATE team_members SET role = ? WHERE team_id = ? AND user_id = ? AND role <> 'owner'", input.Role, teamID, targetID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	if n, err := res.RowsAffected(); err != nil {
		internalError(w, r, err)
		return
	} else if n == 0 {
		var one int
		err := h.db.QueryRowContext(r.Context(), "SELECT 1 FROM team_members WHERE team_id = ? AND user_id = ? AND role <> 'owner'", teamID, targetID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "member not found")
			return
		}
		if err != nil {
			internalError(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"team_id": teamID, "user_id": targetID, "role": input.Role})
}

// RemoveMember удаляет участника: создатель — любого, кроме себя, администратор — только member.
// Участника с задачами в команде удалить нельзя, иначе исполнитель окажется вне команды.
// @Summary Удаление участника из команды
// @Description owner удаляет любого, кроме себя; admin — только member. Участника, назначенного исполнителем задач команды, удалить нельзя.
// @Tags teams
// @Param id path int true "Команда"
// @Param user_id path int true "Участник"
// @Success 204
// @Failure 400 {object} object{error=string} "invalid request"
// @Failure 401 {object} object{error=string} "unauthorized"
// @Failure 403 {object} object{error=string} "forbidden"
// @Failure 404 {object} object{error=string} "team not found или member not found"
// @Failure 409 {object} object{error=string} "member has assigned tasks"
// @Security Bearer
// @Router /api/v1/teams/{id}/members/{user_id} [delete]
func (h *Handler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	teamID, targetID, callerRole, targetRole, ok := h.teamMemberTarget(w, r)
	if !ok {
		return
	}
	if targetRole == "owner" || callerRole == "member" || callerRole == "admin" && targetRole != "member" {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	result, err := h.db.ExecContext(r.Context(), `DELETE FROM team_members WHERE team_id = ? AND user_id = ? AND role <> 'owner'
		AND NOT EXISTS (SELECT 1 FROM tasks WHERE team_id = ? AND assignee_id = ?)`, teamID, targetID, teamID, targetID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	if affected, err := result.RowsAffected(); err != nil || affected == 0 {
		if err != nil {
			internalError(w, r, err)
			return
		}
		writeError(w, http.StatusConflict, "member has assigned tasks")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
