package config

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestRecipeBindsLimitsToConfiguredModels(t *testing.T) {
	const document = `{"schema":"hypermesh.conductor.recipe.v1","max_task_output_tokens":13,"cheap":{"model":"a","max_output_tokens":5},"frontier":{"model":"b","max_output_tokens":8,"context_window_tokens":32}}`
	path := filepath.Join(t.TempDir(), "recipe.json")
	if err := os.WriteFile(path, []byte(document), 0600); err != nil {
		t.Fatal(err)
	}
	cheap, frontier := Tier{Model: "a"}, Tier{Model: "b", InputCounter: "vllm"}
	if err := loadRecipe(path, &cheap, &frontier); err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(document)))
	if frontier.ContextWindow != 32 || cheap.ContextWindow != 0 || cheap.TaskOutputLimit != 13 || frontier.TaskOutputLimit != 13 || cheap.OutputLimit != 5 || frontier.OutputLimit != 8 || cheap.RecipeDigest != digest || frontier.RecipeDigest != digest {
		t.Fatal(cheap, frontier)
	}
	frontier.Model = "c"
	if err := loadRecipe(path, &cheap, &frontier); err == nil {
		t.Fatal("accepted mismatched model")
	}
}
