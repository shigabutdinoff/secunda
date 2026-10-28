package httpapi

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/mysql"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/stretchr/testify/suite"
	"github.com/testcontainers/testcontainers-go"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"
)

type TeamStatsIntegrationSuite struct {
	suite.Suite
	container *tcmysql.MySQLContainer
	db        *sql.DB
	router    http.Handler
	handler   *Handler
	teamID    uint64
	emptyID   uint64
	users     map[string]uint64
}

func TestTeamStatsIntegrationSuite(t *testing.T) {
	suite.Run(t, new(TeamStatsIntegrationSuite))
}

func (s *TeamStatsIntegrationSuite) SetupSuite() {
	ctx := s.T().Context()
	container, err := tcmysql.Run(ctx, "mysql:8.4", tcmysql.WithDatabase("secunda"), tcmysql.WithUsername("test"), tcmysql.WithPassword("test"))
	s.Require().NoError(err)
	s.container = container
	dsn, err := container.ConnectionString(ctx, "parseTime=true")
	s.Require().NoError(err)
	m, err := migrate.New("file://../../migrations", "mysql://"+dsn)
	s.Require().NoError(err)
	s.Require().NoError(m.Up())
	_, _ = m.Close()
	s.db, err = sql.Open("mysql", dsn)
	s.Require().NoError(err)
	s.handler = NewHandler(s.db, nil, testSecret)
	r := chi.NewRouter()
	r.Route("/api/v1/teams", func(r chi.Router) {
		r.Use(s.handler.Authenticate)
		r.Get("/{team_id}/stats", s.handler.GetTeamStats)
	})
	s.router = r
	s.seed()
}

func (s *TeamStatsIntegrationSuite) TearDownSuite() {
	if s.db != nil {
		_ = s.db.Close()
	}
	if s.container != nil {
		s.Require().NoError(testcontainers.TerminateContainer(s.container))
	}
}

func (s *TeamStatsIntegrationSuite) seed() {
	s.users = make(map[string]uint64)
	for _, name := range []string{"owner", "admin", "member", "outsider", "first", "second", "third", "fourth"} {
		result, err := s.db.ExecContext(s.T().Context(), "INSERT INTO users (email, password_hash, name) VALUES (?, 'unused', ?)", name+"@example.com", name)
		s.Require().NoError(err)
		id, err := result.LastInsertId()
		s.Require().NoError(err)
		s.users[name] = uint64(id)
	}
	s.teamID = s.createTeam("main", "owner", "admin", "member")
	s.emptyID = s.createTeam("empty", "owner")
	otherID := s.createTeam("other", "owner")
	s.addTask(s.teamID, "todo", 0, "NOW(), NULL")
	s.addTask(s.teamID, "in_progress", 0, "NOW(), NULL")
	for _, item := range []struct {
		name  string
		count int
	}{{"first", 3}, {"second", 2}, {"third", 1}, {"fourth", 1}} {
		for range item.count {
			s.addTask(s.teamID, "done", s.users[item.name], "NOW() - INTERVAL 100 SECOND, NOW() - INTERVAL 50 SECOND")
		}
	}
	s.addTask(s.teamID, "done", 0, "NOW() - INTERVAL 100 SECOND, NOW() - INTERVAL 50 SECOND")
	s.addTask(s.teamID, "done", s.users["first"], "NOW() - INTERVAL 40 DAY, NOW() - INTERVAL 39 DAY")
	s.addTask(otherID, "done", s.users["fourth"], "NOW() - INTERVAL 10 DAY, NOW() - INTERVAL 9 DAY")
	var taskID uint64
	s.Require().NoError(s.db.QueryRow("SELECT MIN(id) FROM tasks WHERE team_id = ?", s.teamID).Scan(&taskID))
	for range 3 {
		_, err := s.db.Exec("INSERT INTO task_comments (task_id, user_id, content) VALUES (?, ?, 'comment')", taskID, s.users["owner"])
		s.Require().NoError(err)
	}
	_, err := s.db.Exec("INSERT INTO task_comments (task_id, user_id, content) SELECT id, ?, 'other' FROM tasks WHERE team_id = ?", s.users["owner"], otherID)
	s.Require().NoError(err)
}

func (s *TeamStatsIntegrationSuite) createTeam(name, owner string, others ...string) uint64 {
	result, err := s.db.Exec("INSERT INTO teams (name, created_by) VALUES (?, ?)", name, s.users[owner])
	s.Require().NoError(err)
	id, err := result.LastInsertId()
	s.Require().NoError(err)
	for _, user := range append([]string{owner}, others...) {
		role := "member"
		switch user {
		case owner:
			role = "owner"
		case "admin":
			role = "admin"
		}
		_, err = s.db.Exec("INSERT INTO team_members (team_id, user_id, role) VALUES (?, ?, ?)", id, s.users[user], role)
		s.Require().NoError(err)
	}
	return uint64(id)
}

func (s *TeamStatsIntegrationSuite) addTask(teamID uint64, status string, assignee uint64, dates string) {
	query := fmt.Sprintf("INSERT INTO tasks (team_id, title, description, status, created_by, assignee_id, created_at, closed_at) VALUES (?, 'task', '', ?, ?, NULLIF(?, 0), %s)", dates)
	_, err := s.db.Exec(query, teamID, status, s.users["owner"], assignee)
	s.Require().NoError(err)
}

func (s *TeamStatsIntegrationSuite) TestReportAndAccess() {
	response := s.request("owner", fmt.Sprintf("/api/v1/teams/%d/stats", s.teamID))
	s.Require().Equal(http.StatusOK, response.Code, response.Body.String())
	var actual map[string]any
	s.Require().NoError(json.Unmarshal(response.Body.Bytes(), &actual))
	expected := map[string]any{
		"by_status": map[string]any{"todo": float64(1), "in_progress": float64(1), "done": float64(9)},
		"top_assignees": []any{
			map[string]any{"user_id": float64(s.users["first"]), "name": "first", "closed": float64(3)},
			map[string]any{"user_id": float64(s.users["second"]), "name": "second", "closed": float64(2)},
			map[string]any{"user_id": float64(s.users["third"]), "name": "third", "closed": float64(1)},
		},
		"avg_close_seconds": float64(86800) / 9,
		"comments":          float64(3),
	}
	s.InDelta(float64(86800)/9, actual["avg_close_seconds"], 0.000001)
	delete(actual, "avg_close_seconds")
	delete(expected, "avg_close_seconds")
	s.Equal(expected, actual)
	s.Equal(http.StatusOK, s.request("admin", fmt.Sprintf("/api/v1/teams/%d/stats", s.teamID)).Code)
	s.Equal(http.StatusForbidden, s.request("member", fmt.Sprintf("/api/v1/teams/%d/stats", s.teamID)).Code)
	s.Equal(http.StatusNotFound, s.request("outsider", fmt.Sprintf("/api/v1/teams/%d/stats", s.teamID)).Code)
	for _, id := range []string{"0", "invalid"} {
		s.Equal(http.StatusBadRequest, s.request("owner", "/api/v1/teams/"+id+"/stats").Code)
	}
	empty := s.request("owner", fmt.Sprintf("/api/v1/teams/%d/stats", s.emptyID))
	s.Require().Equal(http.StatusOK, empty.Code, empty.Body.String())
	s.JSONEq(`{"by_status":{"todo":0,"in_progress":0,"done":0},"top_assignees":[],"avg_close_seconds":null,"comments":0}`, empty.Body.String())
}

func (s *TeamStatsIntegrationSuite) request(user, path string) *httptest.ResponseRecorder {
	token, err := s.handler.issueToken(s.users[user])
	s.Require().NoError(err)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	s.router.ServeHTTP(response, req)
	return response
}
