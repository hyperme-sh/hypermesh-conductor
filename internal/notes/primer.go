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
	return strings.TrimSpace(trimToBudget(b.String(), budget)), nil
}

// trimToBudget drops trailing lines, then shortens the last line, until the
// estimate fits. Each pass keeps a strictly shorter string: stripping only the
// final newline does not, because the builder already ends with one.
func trimToBudget(text string, budget int) string {
	if budget < 0 {
		budget = 0
	}
	for compact.EstimateTokens(text) > budget {
		trimmed := strings.TrimRight(text, "\n")
		cut := strings.LastIndex(trimmed, "\n")
		if cut < 0 {
			return truncateToBudget(trimmed, budget)
		}
		next := trimmed[:cut+1]
		if len(next) >= len(text) {
			return truncateToBudget(trimmed, budget)
		}
		text = next
	}
	return text
}

func truncateToBudget(text string, budget int) string {
	if budget <= 0 || text == "" {
		return ""
	}
	runes := []rune(text)
	maxRunes := budget * 4
	if maxRunes < len(runes) {
		runes = runes[:maxRunes]
	}
	for len(runes) > 0 && compact.EstimateTokens(string(runes)) > budget {
		runes = runes[:len(runes)-1]
	}
	return string(runes)
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
