package subagent

import "fmt"

// UnknownAgentError is returned when a requested agent is not found.
type UnknownAgentError struct {
	Name string
}

func (e UnknownAgentError) Error() string {
	return fmt.Sprintf("subagent: unknown agent %q", e.Name)
}
