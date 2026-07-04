package task

// ManagerState is a serializable snapshot of a TaskManager.
type ManagerState struct {
	Tasks []Task `json:"tasks"`
}
