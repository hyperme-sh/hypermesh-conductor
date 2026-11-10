package config

import (
	"os"
	"path/filepath"
	"strings"
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
	base["listen"] = "0.0.0.0:8787"
	base["listen_mode"] = "network"
	if _, err := Load(write(t, dir, "network-no-tls.json", base)); err == nil || !strings.Contains(err.Error(), "tls.cert_file") {
		t.Fatal(err)
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

func TestNetworkListenRequiresTLSAndProxyToken(t *testing.T) {
	dir := t.TempDir()
	cert := filepath.Join(dir, "cert.pem")
	key := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(cert, []byte("cert"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := example(t)
	base["listen"] = "0.0.0.0:8787"
	base["listen_mode"] = "network"
	base["tls"] = map[string]any{"cert_file": cert, "key_file": key}
	cfg, err := Load(write(t, dir, "network.json", base))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenMode != ListenModeNetwork || cfg.TLS.CertFile != cert || cfg.Listen != "0.0.0.0:8787" {
		t.Fatalf("%+v", cfg)
	}
	t.Setenv(ProxyTokenEnv, "")
	if err := cfg.RequireListen(); err == nil || !strings.Contains(err.Error(), ProxyTokenEnv) {
		t.Fatal(err)
	}
	t.Setenv(ProxyTokenEnv, "proxy-token")
	if err := cfg.RequireListen(); err != nil {
		t.Fatal(err)
	}
	loopback := example(t)
	loopback["tls"] = map[string]any{"cert_file": cert, "key_file": key}
	if _, err := Load(write(t, dir, "loopback-tls.json", loopback)); err == nil || !strings.Contains(err.Error(), "listen_mode network") {
		t.Fatal(err)
	}
}

func TestGatewayTierRequiresCatalogID(t *testing.T) {
	dir := t.TempDir()
	base := example(t)
	cheap := base["tiers"].(map[string]any)["cheap"].(map[string]any)
	cheap["gateway"] = true
	if _, err := Load(write(t, dir, "alias.json", base)); err == nil || !strings.Contains(err.Error(), "llama-3.1-8b-q4") {
		t.Fatal(err)
	}
	cheap["model"] = "llama-3.1-8b-q4"
	cfg, err := Load(write(t, dir, "catalog.json", base))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Cheap.Gateway || cfg.Cheap.Model != "llama-3.1-8b-q4" {
		t.Fatalf("%+v", cfg.Cheap)
	}
	cheap["model"] = "local-small"
	cheap["catalog_model"] = "llama-3.1-8b-q4"
	cfg, err = Load(write(t, dir, "mapped.json", base))
	if err != nil || cfg.Cheap.Model != "llama-3.1-8b-q4" {
		t.Fatal(err, cfg.Cheap.Model)
	}
	cheap["catalog_model"] = "not-a-catalog-id"
	if _, err := Load(write(t, dir, "bad-map.json", base)); err == nil {
		t.Fatal("expected catalog_model rejection")
	}
	delete(cheap, "catalog_model")
	cheap["provider"] = ProviderAnthropic
	cheap["model"] = "llama-3.1-8b-q4"
	if _, err := Load(write(t, dir, "anthropic.json", base)); err == nil || !strings.Contains(err.Error(), "openai") {
		t.Fatal(err)
	}
}

func TestOmittedTierLoadsFromEndpointsFile(t *testing.T) {
	dir := t.TempDir()
	frontier := map[string]any{"base_url": "https://frontier.example/v1", "model": "frontier-only"}
	cheap := map[string]any{"base_url": "http://127.0.0.1:8080/v1", "model": "cheap-only"}
	cfg, err := Load(writeEndpoints(t, dir, "frontier.json", map[string]any{"frontier": frontier}))
	if err != nil || cfg.Cheap.Configured() || cfg.Frontier.Model != "frontier-only" {
		t.Fatal(err, cfg.Cheap.Configured(), cfg.Frontier.Model)
	}
	if class, err := cfg.FallbackClass(); err != nil || class != FrontierFallbackClass {
		t.Fatal(class, err)
	}
	cfg, err = Load(writeEndpoints(t, dir, "cheap.json", map[string]any{"cheap": cheap}))
	if err != nil || cfg.Frontier.Configured() || cfg.Cheap.Model != "cheap-only" {
		t.Fatal(err, cfg.Frontier.Configured(), cfg.Cheap.Model)
	}
	if class, err := cfg.FallbackClass(); err != nil || class != FallbackClass {
		t.Fatal(class, err)
	}
	if _, err := Load(writeEndpoints(t, dir, "none.json", map[string]any{})); err == nil || !strings.Contains(err.Error(), "cheap or frontier") {
		t.Fatal(err)
	}
	if _, err := Load(writeEndpoints(t, dir, "bad-cheap.json", map[string]any{"cheap": "nope", "frontier": frontier})); err == nil || !strings.Contains(err.Error(), "tiers.cheap must be an object") {
		t.Fatal(err)
	}
	if _, err := Load(writeEndpoints(t, dir, "bad-frontier.json", map[string]any{"cheap": cheap, "frontier": map[string]any{}})); err == nil || !strings.Contains(err.Error(), "tiers.frontier.base_url") {
		t.Fatal(err)
	}
	if _, err := Load(writeEndpoints(t, dir, "null-frontier.json", map[string]any{"cheap": cheap, "frontier": nil})); err == nil || !strings.Contains(err.Error(), "tiers.frontier must be an object") {
		t.Fatal(err)
	}
}

func writeEndpoints(t *testing.T, dir, name string, tiers map[string]any) string {
	t.Helper()
	return write(t, dir, name, map[string]any{
		"listen": "127.0.0.1:8787",
		"tiers":  tiers,
		"prefix": map[string]any{"system": "You are Conductor.", "tools": []any{}},
	})
}

func TestFallbackClassUsesTheFirstConfiguredTier(t *testing.T) {
	if class, err := (Config{Cheap: Tier{BaseURL: "http://127.0.0.1:9/v1"}}).FallbackClass(); err != nil || class != FallbackClass {
		t.Fatal(class, err)
	}
	if class, err := (Config{Frontier: Tier{Model: "frontier"}}).FallbackClass(); err != nil || class != FrontierFallbackClass {
		t.Fatal(class, err)
	}
	if _, err := (Config{}).FallbackClass(); err == nil || !strings.Contains(err.Error(), "no tiers are configured") {
		t.Fatal(err)
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
