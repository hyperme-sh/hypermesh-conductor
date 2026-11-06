package routing

import "testing"

func TestCheapAndFrontierClasses(t *testing.T) {
	cheap := []string{"parse", "  Parse  ", "extract", "extraction", "index", "indexing", "classify", "classification", "embed", "embeddings", "summarize", "summary", "status", "status update", "status_update", "write-status"}
	for _, label := range cheap {
		decision, err := Route(label, "", nil)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if decision.Tier != Cheap || DefaultTiers[decision.JobClass] != Cheap {
			t.Fatalf("%s routed to %s/%s", label, decision.JobClass, decision.Tier)
		}
		if decision.SuggestionAccepted != nil {
			t.Fatalf("suggestion should be unset")
		}
	}
	frontier := []string{"plan", "planning", "architecture", "architect", "tradeoff", "ambiguous tradeoffs", "code", "code generation", "implement", "review", "code review", "code-review"}
	for _, label := range frontier {
		decision, err := Route(label, "", nil)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if decision.Tier != Frontier {
			t.Fatalf("%s tier %s", label, decision.Tier)
		}
	}
}

func TestUpgradeIsRejected(t *testing.T) {
	decision, err := Route("parse", "frontier", nil)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Tier != Cheap || decision.SuggestionAccepted == nil || *decision.SuggestionAccepted {
		t.Fatalf("%+v", decision)
	}
	if decision.Reason != "rejected out-of-policy upgrade from cheap to frontier for parse" {
		t.Fatal(decision.Reason)
	}
}

func TestLocalAliasAndDowngrade(t *testing.T) {
	matched, err := Route("status update", "local", nil)
	if err != nil || matched.Tier != Cheap || matched.SuggestionAccepted == nil || !*matched.SuggestionAccepted {
		t.Fatalf("%+v %v", matched, err)
	}
	kept, err := Route("code review", "cheap", nil)
	if err != nil || kept.JobClass != "review" || kept.Tier != Frontier || *kept.SuggestionAccepted {
		t.Fatalf("%+v %v", kept, err)
	}
	if kept.Reason != "rejected tier change from frontier to cheap for review; policy tier stands" {
		t.Fatal(kept.Reason)
	}
}

func TestProfileOverride(t *testing.T) {
	decision, err := Route("summarize", "cheap", map[string]string{"summarize": "frontier"})
	if err != nil || decision.Tier != Frontier || *decision.SuggestionAccepted {
		t.Fatalf("%+v %v", decision, err)
	}
	other, err := Route("parse", "", map[string]string{"summarize": "frontier"})
	if err != nil || other.Tier != Cheap {
		t.Fatal(other)
	}
}

func TestUnknownClassAndTier(t *testing.T) {
	if _, err := Route("dance", "", nil); err == nil {
		t.Fatal("expected unknown class")
	}
	if _, err := Route("   ", "", nil); err == nil {
		t.Fatal("expected empty class")
	}
	if _, err := Route("parse", "giant", nil); err == nil {
		t.Fatal("expected unknown tier")
	}
}
