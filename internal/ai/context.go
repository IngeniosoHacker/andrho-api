package ai

import _ "embed"

// ProjectContext is context/andrho.md: what AndRho is, what the site and
// the dashboard do today and how the AI should talk. Every prompt that
// speaks to or about a client starts from it (WithContext).
//
//go:embed context/andrho.md
var ProjectContext string

// WithContext puts the project context in front of a task's system prompt.
func WithContext(task string) string {
	return ProjectContext + "\n\n---\n\n# Tu tarea\n\n" + task
}
