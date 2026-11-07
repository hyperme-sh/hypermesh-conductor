package notes

import (
	"fmt"
	"strings"

	"unicode/utf8"
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
	violations, err := goalViolations(store.db)
	if err != nil {
		return "", err
	}
	blocked := map[string]struct{}{}
	for _, violation := range violations {
		blocked[violation.GoalID] = struct{}{}
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
	used := 0
	appendLine := func(line string) bool {
		count := utf8.RuneCountInString(line)
		if (used+count)/4 > budget {
			return false
		}
		b.WriteString(line)
		used += count
		return true
	}
	const header = "# Session primer\n"
	if !appendLine(header) {
		return strings.TrimSpace(header[:budget*4]), nil
	}
	for _, goal := range goals {
		if !appendLine(fmt.Sprintf("- goal (%s): %s\n", goal.Status, goal.Title)) {
			return strings.TrimSpace(b.String()), nil
		}
		steps, err := linkedSteps(store, goal.ID, byID)
		if err != nil {
			return "", err
		}
		for _, step := range steps {
			if Closed(step.Status) {
				continue
			}
			if !appendLine(fmt.Sprintf("  - step (%s): %s\n", step.Status, step.Title)) {
				return strings.TrimSpace(b.String()), nil
			}
		}
	}
	for _, fact := range facts {
		if !appendLine(fmt.Sprintf("- fact: %s\n", fact.Title)) {
			return strings.TrimSpace(b.String()), nil
		}
	}
	return strings.TrimSpace(b.String()), nil
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
