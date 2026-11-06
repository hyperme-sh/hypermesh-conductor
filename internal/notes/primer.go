package notes

import (
	"fmt"
	"strings"

	"github.com/hyperme-sh/hypermesh-conductor/internal/compact"
)

// Primer is a short Markdown list of open goals, their open steps, and facts.
// A goal that is marked done while a step is open stays in the primer.
func Primer(store *Store, budget int) (string, error) {
	if budget <= 0 {
		budget = 200
	}
	notes, err := store.All()
	if err != nil {
		return "", err
	}
	violations, err := store.Violations()
	if err != nil {
		return "", err
	}
	blocked := map[string]struct{}{}
	for _, line := range violations {
		// "goal <id> cannot close..."
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			blocked[fields[1]] = struct{}{}
		}
	}
	byID := map[string]Note{}
	var goals, facts []Note
	for _, note := range notes {
		byID[note.ID] = note
		switch note.Type {
		case "goal":
			if !Closed(note.Status) || hasKey(blocked, note.ID) {
				goals = append(goals, note)
			}
		case "fact":
			if !Closed(note.Status) {
				facts = append(facts, note)
			}
		}
	}
	if len(goals) == 0 && len(facts) == 0 {
		return "", nil
	}
	var b strings.Builder
	b.WriteString("# Session primer\n")
	for _, goal := range goals {
		fmt.Fprintf(&b, "- goal (%s): %s\n", goal.Status, goal.Title)
		steps, err := linkedSteps(store, goal.ID, byID)
		if err != nil {
			return "", err
		}
		for _, step := range steps {
			if Closed(step.Status) {
				continue
			}
			fmt.Fprintf(&b, "  - step (%s): %s\n", step.Status, step.Title)
		}
	}
	for _, fact := range facts {
		fmt.Fprintf(&b, "- fact: %s\n", fact.Title)
	}
	text := b.String()
	for compact.EstimateTokens(text) > budget && strings.Count(text, "\n") > 2 {
		text = text[:strings.LastIndex(text, "\n")]
		text += "\n"
	}
	return strings.TrimSpace(text), nil
}

func linkedSteps(store *Store, goal string, byID map[string]Note) ([]Note, error) {
	ids, err := store.Neighbors(goal)
	if err != nil {
		return nil, err
	}
	var steps []Note
	for _, id := range ids {
		note, ok := byID[id]
		if ok && note.Type == "steps" {
			steps = append(steps, note)
		}
	}
	return steps, nil
}

func hasKey(m map[string]struct{}, key string) bool {
	_, ok := m[key]
	return ok
}
