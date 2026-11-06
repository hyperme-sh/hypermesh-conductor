package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExampleConfigLoads(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "examples", "endpoints.json"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Cheap.Model != "local-small" || cfg.Frontier.Model != "frontier" {
		t.Fatal(cfg.Cheap.Model, cfg.Frontier.Model)
	}
	if cfg.Cheap.APIKeyEnv != "CONDUCTOR_CHEAP_API_KEY" {
		t.Fatal(cfg.Cheap.APIKeyEnv)
	}
	if cfg.Listen != DefaultListen {
		t.Fatal(cfg.Listen)
	}
	hash, err := cfg.Prefix.SHA256()
	if err != nil || hash == "" {
		t.Fatal(err, hash)
	}
}

func TestRejectsSecretsAndBadURLs(t *testing.T) {
	dir := t.TempDir()
	base := example(t)
	base["tiers"].(map[string]any)["cheap"].(map[string]any)["api_key"] = "sk-test"
	if _, err := Load(write(t, dir, "key.json", base)); err == nil {
		t.Fatal("expected api_key rejection")
	}
	base = example(t)
	base["tiers"].(map[string]any)["frontier"].(map[string]any)["base_url"] = "https://user:secret@example.test/v1"
	if _, err := Load(write(t, dir, "url.json", base)); err == nil {
		t.Fatal("expected credential rejection")
	}
	base = example(t)
	base["tiers"].(map[string]any)["cheap"].(map[string]any)["base_url"] = "http://127.0.0.1:8080/v1/chat/completions"
	if _, err := Load(write(t, dir, "method.json", base)); err == nil {
		t.Fatal("expected API root rejection")
	}
	base = example(t)
	base["tiers"].(map[string]any)["cheap"].(map[string]any)["base_url"] = "http://example.test/v1"
	if _, err := Load(write(t, dir, "plain.json", base)); err == nil {
		t.Fatal("expected https requirement")
	}
	base = example(t)
	base["aiuditor"] = map[string]any{"mode": "http", "url": "http://aiuditor.example.test/review", "fail_closed": true}
	if _, err := Load(write(t, dir, "aiu.json", base)); err == nil {
		t.Fatal("expected aiuditor https requirement")
	}
	base = example(t)
	base["tiers"].(map[string]any)["cheap"].(map[string]any)["base_url"] = "https://example.test/v1"
	base["aiuditor"] = map[string]any{"mode": "http", "url": "http://127.0.0.1:9/review", "fail_closed": true}
	if _, err := Load(write(t, dir, "https.json", base)); err != nil {
		t.Fatal(err)
	}
	base = example(t)
	base["listen"] = "0.0.0.0:8787"
	if _, err := Load(write(t, dir, "listen.json", base)); err == nil {
		t.Fatal("expected loopback rejection")
	}
	base = example(t)
	base["class_tiers"] = map[string]any{"dance": "cheap"}
	if _, err := Load(write(t, dir, "class.json", base)); err == nil {
		t.Fatal("expected unknown class")
	}
	base = example(t)
	base["cost_proxy_rates"] = map[string]any{"cheap": map[string]any{"input_per_million": 1, "output_per_million": 2}}
	if _, err := Load(write(t, dir, "rate.json", base)); err == nil {
		t.Fatal("expected source requirement")
	}
}

func example(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "examples", "endpoints.json"))
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := jsonUnmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func write(t *testing.T, dir, name string, payload map[string]any) string {
	t.Helper()
	raw, err := jsonMarshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
