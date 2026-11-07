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
	// Only fetch display fields for the bounded prefix. The candidate rows
	// carry SQLite rowids so sorting never copies note bodies or titles.
	fieldLimit := min(budget, int(maxNoteBytes))*4 + 4
	rows, err := store.db.Query(`
WITH edges(goal, step) AS (
 SELECT g.rowid,s.rowid FROM links l JOIN notes g ON g.id=l.src JOIN notes s ON s.id=l.dst
 WHERE g.type='goal' AND s.type='steps' AND s.status NOT IN ('done','closed','drop','dropped')
 UNION
 SELECT g.rowid,s.rowid FROM links l JOIN notes g ON g.id=l.dst JOIN notes s ON s.id=l.src
 WHERE g.type='goal' AND s.type='steps' AND s.status NOT IN ('done','closed','drop','dropped')
), goals AS (
 SELECT rowid AS rid FROM notes WHERE type='goal' AND
 (status NOT IN ('done','closed','drop','dropped') OR
 (status IN ('done','closed') AND rowid IN (SELECT goal FROM edges)))
), entries(category,parent,child,kind) AS (
 SELECT 0,rid,rid,'goal' FROM goals UNION ALL
 SELECT 0,goal,step,'step' FROM edges WHERE goal IN (SELECT rid FROM goals) UNION ALL
 SELECT 1,rowid,rowid,'fact' FROM notes WHERE type='fact' AND status NOT IN ('done','closed','drop','dropped')
)
SELECT kind,substr(n.status,1,?),substr(n.title,1,?) FROM entries
JOIN notes p ON p.rowid=parent JOIN notes n ON n.rowid=child
ORDER BY category,p.step_order,p.id,kind,n.id LIMIT ?`, fieldLimit, fieldLimit, fieldLimit)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	if !rows.Next() {
		return "", rows.Err()
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
	for {
		var kind, status, title string
		if err := rows.Scan(&kind, &status, &title); err != nil {
			return "", err
		}
		var line string
		switch kind {
		case "goal":
			line = fmt.Sprintf("- goal (%s): %s\n", status, title)
		case "step":
			line = fmt.Sprintf("  - step (%s): %s\n", status, title)
		default:
			line = fmt.Sprintf("- fact: %s\n", title)
		}
		if !appendLine(line) || !rows.Next() {
			break
		}
	}
	return strings.TrimSpace(b.String()), rows.Err()
}
