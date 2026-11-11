package proxy

import (
	"github.com/hyperme-sh/hypermesh-conductor/internal/config"
	"os"
	"path/filepath"
	"testing"
)

func TestRecipeReloadPreservesLastValidSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	recipe := filepath.Join(dir, "recipe.json")
	raw := `{"tiers":{"frontier":{"base_url":"http://127.0.0.1:1/v1","model":"fixture"}},"prefix":{"system":"system","tools":[]},"recipe_file":"recipe.json"}`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(recipe, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"schema":"hypermesh.conductor.recipe.v1","frontier":{"model":"fixture","max_output_tokens":8}}`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p := New(cfg, nil, nil)
	p.EnableRecipeReload(path)
	original := p.recipes.active.Load()
	write(`{"schema":"broken"}`)
	if p.recipes.reload() == nil || p.recipes.active.Load() != original {
		t.Fatal("invalid reload changed snapshot")
	}
	write(`{"schema":"hypermesh.conductor.recipe.v1","frontier":{"model":"fixture","max_output_tokens":4}}`)
	if err := p.recipes.reload(); err != nil {
		t.Fatal(err)
	}
	if p.recipes.active.Load().Config.Frontier.OutputLimit != 4 || original.Config.Frontier.OutputLimit != 8 {
		t.Fatal("in-flight snapshot was mutated")
	}
	if p.recipes.active.Load().Config.Frontier.RecipeDigest == original.Config.Frontier.RecipeDigest {
		t.Fatal("revision did not change")
	}
}
