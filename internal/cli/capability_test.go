package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCapabilityIndexCommandAndProposal(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("LOCALAPPDATA", cache)
	dir := t.TempDir()
	draft := t.TempDir()
	files, err := filepath.Glob(filepath.Join("..", "..", "docs", "examples", "capability-hosts", "*.md"))
	if err != nil || len(files) != 4 {
		t.Fatal("missing examples", err)
	}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, dst := range []string{dir, draft} {
			if err := os.WriteFile(filepath.Join(dst, filepath.Base(file)), raw, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	args := []string{"index", "capabilities", "--notes", dir, "--db", filepath.Join(cache, "baseline.db")}
	var out, stderr bytes.Buffer
	if code := Main(args, &out, &stderr); code != 2 || out.Len() != 0 {
		t.Fatal("scope was optional")
	}
	args = append(args, "--scope", "org/example/site/lab")
	out.Reset()
	stderr.Reset()
	if code := Main(args, &out, &stderr); code != 0 {
		t.Fatal(code, stderr.String())
	}
	var entries []map[string]any
	if err := json.Unmarshal(out.Bytes(), &entries); err != nil || len(entries) != 4 {
		t.Fatal("invalid scoped export", err)
	}
	if strings.Contains(out.String(), "Illustrative fixture") || strings.Contains(out.String(), "```capability-json") {
		t.Fatal("raw note bodies exported")
	}
	path := filepath.Join(draft, "summarize-counters.md")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Change only this verb's revision, preserving its noun reference revision.
	changed := strings.Replace(string(raw), `"revision": "r1"`, `"revision": "r2"`, 1)
	tightened := strings.Replace(changed, `"maximum": 60`, `"maximum": 30`, 1)
	if err := os.WriteFile(path, []byte(tightened), 0o600); err != nil {
		t.Fatal(err)
	}
	args = append(args, "--proposal", draft, "--proposal-db", filepath.Join(cache, "draft.db"))
	out.Reset()
	stderr.Reset()
	if code := Main(args, &out, &stderr); code != 0 {
		t.Fatal(code, stderr.String())
	}
	widened := strings.Replace(changed, `"maximum": 60`, `"maximum": 90`, 1)
	if err := os.WriteFile(path, []byte(widened), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	stderr.Reset()
	if code := Main(args, &out, &stderr); code != 2 || out.Len() != 0 {
		t.Fatal("unsafe proposal exported")
	}
	unchanged, err := os.ReadFile(filepath.Join(dir, "summarize-counters.md"))
	if err != nil || string(unchanged) != string(raw) {
		t.Fatal("baseline was modified")
	}
}
