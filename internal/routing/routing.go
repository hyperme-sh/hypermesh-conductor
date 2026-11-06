// Package routing chooses a cheap or frontier tier from the job class.
//
// Rules are the authority. A suggestion may match the policy tier. It cannot
// move a step onto a higher tier, and it cannot replace the policy tier with a
// lower one. A failed call stays on the chosen tier.
package routing

import (
	"fmt"
	"strings"
)

const (
	Cheap    = "cheap"
	Frontier = "frontier"
)

// DefaultTiers is the policy table. A profile may replace individual entries.
// A suggestion may not.
var DefaultTiers = map[string]string{
	"parse":        Cheap,
	"extract":      Cheap,
	"index":        Cheap,
	"classify":     Cheap,
	"embed":        Cheap,
	"summarize":    Cheap,
	"status":       Cheap,
	"plan":         Frontier,
	"architecture": Frontier,
	"tradeoff":     Frontier,
	"code":         Frontier,
	"review":       Frontier,
}

var tierRank = map[string]int{Cheap: 0, Frontier: 1}

var aliases = map[string]string{
	"parsing":             "parse",
	"extraction":          "extract",
	"indexing":            "index",
	"classification":      "classify",
	"embedding":           "embed",
	"embeddings":          "embed",
	"summary":             "summarize",
	"summarization":       "summarize",
	"status update":       "status",
	"write status":        "status",
	"planning":            "plan",
	"architect":           "architecture",
	"tradeoffs":           "tradeoff",
	"ambiguous tradeoff":  "tradeoff",
	"ambiguous tradeoffs": "tradeoff",
	"codegen":             "code",
	"code generation":     "code",
	"implement":           "code",
	"code review":         "review",
}

// Decision is the preview of one routing choice. Tier is the policy tier.
type Decision struct {
	JobClass           string
	Tier               string
	Reason             string
	SuggestionTier     *string
	SuggestionAccepted *bool
}

// Error is a rejected class or tier name.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

// NormalizeLabel folds case and separators the same way for classes and tiers.
func NormalizeLabel(value string) string {
	text := strings.ToLower(strings.TrimSpace(value))
	text = strings.NewReplacer("-", " ", "_", " ").Replace(text)
	return strings.Join(strings.Fields(text), " ")
}

// CanonicalClass maps a label onto a known job class.
func CanonicalClass(value string) (string, error) {
	label := NormalizeLabel(value)
	if label == "" {
		return "", &Error{Msg: "job class is empty"}
	}
	if _, ok := DefaultTiers[label]; ok {
		return label, nil
	}
	if aliased, ok := aliases[label]; ok {
		return aliased, nil
	}
	return "", &Error{Msg: fmt.Sprintf("unknown job class '%s'", value)}
}

// CanonicalTier accepts cheap/local and frontier/strong.
func CanonicalTier(value string) (string, error) {
	label := NormalizeLabel(value)
	switch label {
	case Cheap, "local":
		return Cheap, nil
	case Frontier, "strong":
		return Frontier, nil
	default:
		return "", &Error{Msg: fmt.Sprintf("unknown tier '%s'", value)}
	}
}

// Route chooses the policy tier and records whether a suggestion matched.
func Route(jobClass string, suggestionTier string, classTiers map[string]string) (Decision, error) {
	canonical, err := CanonicalClass(jobClass)
	if err != nil {
		return Decision{}, err
	}
	policy := DefaultTiers[canonical]
	if classTiers != nil {
		if override, ok := classTiers[canonical]; ok {
			policy, err = CanonicalTier(override)
			if err != nil {
				return Decision{}, err
			}
		}
	}
	if strings.TrimSpace(suggestionTier) == "" {
		return Decision{
			JobClass: canonical,
			Tier:     policy,
			Reason:   fmt.Sprintf("policy tier for %s is %s", canonical, policy),
		}, nil
	}
	suggested, err := CanonicalTier(suggestionTier)
	if err != nil {
		return Decision{}, err
	}
	if suggested == policy {
		ok := true
		return Decision{
			JobClass:           canonical,
			Tier:               policy,
			Reason:             fmt.Sprintf("suggestion matches policy tier %s for %s", policy, canonical),
			SuggestionTier:     &suggested,
			SuggestionAccepted: &ok,
		}, nil
	}
	var reason string
	if tierRank[suggested] > tierRank[policy] {
		reason = fmt.Sprintf("rejected out-of-policy upgrade from %s to %s for %s", policy, suggested, canonical)
	} else {
		reason = fmt.Sprintf("rejected tier change from %s to %s for %s; policy tier stands", policy, suggested, canonical)
	}
	no := false
	return Decision{
		JobClass:           canonical,
		Tier:               policy,
		Reason:             reason,
		SuggestionTier:     &suggested,
		SuggestionAccepted: &no,
	}, nil
}
