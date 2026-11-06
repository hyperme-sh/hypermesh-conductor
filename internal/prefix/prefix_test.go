package prefix

import "testing"

func TestToolOrderDoesNotChangeTheHash(t *testing.T) {
	first, err := Build("Stay on policy.", []any{
		map[string]any{"type": "function", "function": map[string]any{"name": "record_status", "description": "Record status", "parameters": map[string]any{"type": "object", "required": []any{"task_id"}}}},
		map[string]any{"type": "function", "function": map[string]any{"name": "lookup_note", "description": "Read a note"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Build("Stay on policy.", []any{
		map[string]any{"function": map[string]any{"description": "Read a note", "name": "lookup_note"}, "type": "function"},
		map[string]any{"function": map[string]any{"name": "record_status", "parameters": map[string]any{"required": []any{"task_id"}, "type": "object"}, "description": "Record status"}, "type": "function"},
	})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := first.SHA256()
	b, _ := second.SHA256()
	if a != b {
		t.Fatalf("hashes differ %s %s", a, b)
	}
	if functionName(first.Tools[0].(map[string]any)) != "lookup_note" {
		t.Fatal(first.Tools)
	}
}

func TestSystemTextIsNotRewritten(t *testing.T) {
	spaced, err := Build("Stay on policy. ", nil)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := Build("Stay on policy.", nil)
	if err != nil {
		t.Fatal(err)
	}
	if spaced.System != "Stay on policy. " {
		t.Fatal(spaced.System)
	}
	left, _ := spaced.SHA256()
	right, _ := plain.SHA256()
	if left == right {
		t.Fatal("expected different hashes")
	}
}

func TestAmpersandIsNotEscaped(t *testing.T) {
	raw, err := Canonical(map[string]any{"system": "a & b", "tools": []any{}})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"system":"a & b","tools":[]}` {
		t.Fatal(string(raw))
	}
}

func TestDuplicateToolNames(t *testing.T) {
	_, err := Build("Stay on policy.", []any{
		map[string]any{"type": "function", "function": map[string]any{"name": "record_status"}},
		map[string]any{"type": "function", "function": map[string]any{"name": "record_status"}},
	})
	if err == nil {
		t.Fatal("expected duplicate rejection")
	}
}
