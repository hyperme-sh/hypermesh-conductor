package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

type recipeTier struct {
	Model           string `json:"model"`
	MaxOutputTokens int    `json:"max_output_tokens"`
}

func loadRecipe(path string, cheap, frontier *Tier) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	var recipe struct {
		Schema   string     `json:"schema"`
		Cheap    recipeTier `json:"cheap"`
		Frontier recipeTier `json:"frontier"`
	}
	hash := sha256.New()
	decoder := json.NewDecoder(io.TeeReader(file, hash))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&recipe); err != nil {
		return fmt.Errorf("recipe: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF || recipe.Schema != "hypermesh.conductor.recipe.v1" {
		return fmt.Errorf("invalid recipe document or schema")
	}
	for _, entry := range []struct {
		tier     *Tier
		settings recipeTier
	}{{cheap, recipe.Cheap}, {frontier, recipe.Frontier}} {
		if entry.settings.Model != entry.tier.Model || entry.settings.MaxOutputTokens < 1 || entry.settings.MaxOutputTokens > 2147483647 {
			return fmt.Errorf("recipe %s requires configured model %q and a positive 32-bit output limit", entry.tier.Name, entry.tier.Model)
		}
		entry.tier.RecipeDigest = hex.EncodeToString(hash.Sum(nil))
		entry.tier.OutputLimit = entry.settings.MaxOutputTokens
	}
	return nil
}
