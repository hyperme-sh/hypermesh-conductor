package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

var CaptureID = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

// Captures are explicit local configuration. They are never learned from URLs
// or headers supplied by a client. A restart is required after changing files.
func loadCaptures(value any, configPath string, enforce bool) (map[string]Tier, error) {
	if value == nil {
		return nil, nil
	}
	dir, ok := value.(string)
	if !ok || dir == "" {
		return nil, fmt.Errorf("capture_dir must name a directory")
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(filepath.Dir(configPath), dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	result := map[string]Tier{}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := entry.Name()[:len(entry.Name())-5]
		if !CaptureID.MatchString(id) || !entry.Type().IsRegular() || len(result) >= 64 {
			return nil, fmt.Errorf("invalid captured tool file")
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		if len(data) > 65536 {
			return nil, fmt.Errorf("captured tool file too large")
		}
		var raw map[string]any
		if json.Unmarshal(data, &raw) != nil || raw["schema"] != "hypermesh.conductor.capture.v1" {
			return nil, fmt.Errorf("invalid captured tool schema")
		}
		if err := rejectSecrets(raw, "capture"); err != nil {
			return nil, err
		}
		tier, err := parseTier("frontier", raw)
		if err != nil {
			return nil, fmt.Errorf("capture %s: %w", id, err)
		}
		if enforce || !tier.Direct || tier.Gateway || tier.Auth != AuthPassthrough {
			return nil, fmt.Errorf("captured tools require direct passthrough and no gateway enforcement")
		}
		result[id] = tier
	}
	return result, nil
}
