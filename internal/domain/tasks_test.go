package domain

import "testing"

func TestCanEdit(t *testing.T) {
	assigneeID := uint64(2)
	task := Task{CreatedBy: 1, AssigneeID: &assigneeID}
	title := Patch{Title: true}
	description := Patch{Description: true}
	status := Patch{Status: true}
	assignee := Patch{AssigneeID: true}
	statusAndTitle := Patch{Status: true, Title: true}
	all := Patch{Title: true, Description: true, Status: true, AssigneeID: true}

	tests := []struct {
		name   string
		role   string
		userID uint64
		patch  Patch
		want   bool
	}{
		{name: "owner creator title", role: "owner", userID: 1, patch: title, want: true},
		{name: "owner creator description", role: "owner", userID: 1, patch: description, want: true},
		{name: "owner creator status", role: "owner", userID: 1, patch: status, want: true},
		{name: "owner creator assignee", role: "owner", userID: 1, patch: assignee, want: true},
		{name: "owner creator mixed", role: "owner", userID: 1, patch: statusAndTitle, want: true},
		{name: "owner creator all", role: "owner", userID: 1, patch: all, want: true},
		{name: "owner assignee title", role: "owner", userID: 2, patch: title, want: true},
		{name: "owner assignee description", role: "owner", userID: 2, patch: description, want: true},
		{name: "owner assignee status", role: "owner", userID: 2, patch: status, want: true},
		{name: "owner assignee assignee", role: "owner", userID: 2, patch: assignee, want: true},
		{name: "owner assignee mixed", role: "owner", userID: 2, patch: statusAndTitle, want: true},
		{name: "owner assignee all", role: "owner", userID: 2, patch: all, want: true},
		{name: "owner unrelated title", role: "owner", userID: 3, patch: title, want: true},
		{name: "owner unrelated description", role: "owner", userID: 3, patch: description, want: true},
		{name: "owner unrelated status", role: "owner", userID: 3, patch: status, want: true},
		{name: "owner unrelated assignee", role: "owner", userID: 3, patch: assignee, want: true},
		{name: "owner unrelated mixed", role: "owner", userID: 3, patch: statusAndTitle, want: true},
		{name: "owner unrelated all", role: "owner", userID: 3, patch: all, want: true},
		{name: "admin creator title", role: "admin", userID: 1, patch: title, want: true},
		{name: "admin creator description", role: "admin", userID: 1, patch: description, want: true},
		{name: "admin creator status", role: "admin", userID: 1, patch: status, want: true},
		{name: "admin creator assignee", role: "admin", userID: 1, patch: assignee, want: true},
		{name: "admin creator mixed", role: "admin", userID: 1, patch: statusAndTitle, want: true},
		{name: "admin creator all", role: "admin", userID: 1, patch: all, want: true},
		{name: "admin assignee title", role: "admin", userID: 2, patch: title, want: true},
		{name: "admin assignee description", role: "admin", userID: 2, patch: description, want: true},
		{name: "admin assignee status", role: "admin", userID: 2, patch: status, want: true},
		{name: "admin assignee assignee", role: "admin", userID: 2, patch: assignee, want: true},
		{name: "admin assignee mixed", role: "admin", userID: 2, patch: statusAndTitle, want: true},
		{name: "admin assignee all", role: "admin", userID: 2, patch: all, want: true},
		{name: "admin unrelated title", role: "admin", userID: 3, patch: title, want: true},
		{name: "admin unrelated description", role: "admin", userID: 3, patch: description, want: true},
		{name: "admin unrelated status", role: "admin", userID: 3, patch: status, want: true},
		{name: "admin unrelated assignee", role: "admin", userID: 3, patch: assignee, want: true},
		{name: "admin unrelated mixed", role: "admin", userID: 3, patch: statusAndTitle, want: true},
		{name: "admin unrelated all", role: "admin", userID: 3, patch: all, want: true},
		{name: "member creator title", role: "member", userID: 1, patch: title, want: true},
		{name: "member creator description", role: "member", userID: 1, patch: description, want: true},
		{name: "member creator status", role: "member", userID: 1, patch: status, want: true},
		{name: "member creator assignee", role: "member", userID: 1, patch: assignee, want: true},
		{name: "member creator mixed", role: "member", userID: 1, patch: statusAndTitle, want: true},
		{name: "member creator all", role: "member", userID: 1, patch: all, want: true},
		{name: "member assignee title", role: "member", userID: 2, patch: title, want: false},
		{name: "member assignee description", role: "member", userID: 2, patch: description, want: false},
		{name: "member assignee status", role: "member", userID: 2, patch: status, want: true},
		{name: "member assignee assignee", role: "member", userID: 2, patch: assignee, want: false},
		{name: "member assignee mixed", role: "member", userID: 2, patch: statusAndTitle, want: false},
		{name: "member assignee all", role: "member", userID: 2, patch: all, want: false},
		{name: "member unrelated title", role: "member", userID: 3, patch: title, want: false},
		{name: "member unrelated description", role: "member", userID: 3, patch: description, want: false},
		{name: "member unrelated status", role: "member", userID: 3, patch: status, want: false},
		{name: "member unrelated assignee", role: "member", userID: 3, patch: assignee, want: false},
		{name: "member unrelated mixed", role: "member", userID: 3, patch: statusAndTitle, want: false},
		{name: "member unrelated all", role: "member", userID: 3, patch: all, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CanEdit(tt.role, task, tt.userID, tt.patch); got != tt.want {
				t.Errorf("CanEdit() = %t, want %t", got, tt.want)
			}
		})
	}
}
