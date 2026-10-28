package domain

// Task содержит данные задачи, необходимые для проверки прав редактирования.
type Task struct {
	CreatedBy  uint64
	AssigneeID *uint64
}

// Patch отмечает поля задачи, присутствующие в запросе на изменение.
type Patch struct {
	Title       bool
	Description bool
	Status      bool
	AssigneeID  bool
}

// CanEdit проверяет, может ли пользователь с указанной ролью изменить выбранные поля задачи.
func CanEdit(role string, task Task, userID uint64, patch Patch) bool {
	if role == "owner" || role == "admin" || task.CreatedBy == userID {
		return true
	}
	if role != "member" || task.AssigneeID == nil || *task.AssigneeID != userID {
		return false
	}
	return !patch.Title && !patch.Description && !patch.AssigneeID
}
