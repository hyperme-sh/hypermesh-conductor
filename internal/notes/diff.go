package notes

import (
	"fmt"
	"sort"
	"strings"
)

// MarkdownDiffs proposes link and merge edits. The returned text is a diff.
// It is not applied to the notes directory.
func MarkdownDiffs(list []Note) string {
	byTitle := map[string][]Note{}
	var b strings.Builder
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	for _, note := range list {
		byTitle[fold(note.Title)] = append(byTitle[fold(note.Title)], note)
	}
	for _, group := range byTitle {
		if len(group) < 2 {
			continue
		}
		sort.Slice(group, func(i, j int) bool { return group[i].ID < group[j].ID })
		keep, drop := group[0], group[1]
		if keep.Type != drop.Type {
			continue
		}
		fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n@@\n+%s\n", keep.Path, keep.Path, "Merged with [["+drop.ID+"]].")
		fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n@@\n-status: %s\n+status: dropped\n", drop.Path, drop.Path, drop.Status)
	}
	for _, note := range list {
		linked := knownLinkTargets(note)
		for _, other := range list {
			if other.ID == note.ID {
				continue
			}
			if _, ok := linked[fold(other.ID)]; ok {
				continue
			}
			if _, ok := linked[fold(other.Title)]; ok {
				continue
			}
			if !wordHas(note.Body, other.Title) && !wordHas(note.Body, other.ID) {
				continue
			}
			fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n@@\n+See [[%s]].\n", note.Path, note.Path, other.ID)
			break
		}
	}
	return b.String()
}

// knownLinkTargets is the set of notes this note already points at. Dream
// passes Store.All(), whose Links come from the index, and the body still
// contains the wikilink when that field was not filled.
func knownLinkTargets(note Note) map[string]struct{} {
	linked := map[string]struct{}{}
	add := func(target string) {
		target = fold(target)
		if target == "" {
			return
		}
		linked[target] = struct{}{}
	}
	for _, target := range note.Links {
		add(target)
	}
	for _, target := range wikiLinks(note.Body) {
		add(target)
	}
	return linked
}
