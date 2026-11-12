package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCaptureProfilesAreExplicitAndBound(t *testing.T) {
	dir := t.TempDir()
	profile := `{"schema":"hypermesh.conductor.capture.v1","provider":"openai-compatible","base_url":"https://provider.example/v1","model":"$client","auth":"passthrough","direct":true,"credential_origin":"https://provider.example"}`
	path := filepath.Join(dir, "codex.json")
	if err := os.WriteFile(path, []byte(profile), 0600); err != nil {
		t.Fatal(err)
	}
	profiles, err := loadCaptures(dir, "config.json", false)
	if err != nil || profiles["codex"].CredentialOrigin != "https://provider.example" {
		t.Fatal(profiles, err)
	}
	if _, err := loadCaptures(dir, "config.json", true); err == nil {
		t.Fatal("gateway enforcement accepted direct profile")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "missing"), path); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCaptures(dir, "config.json", false); err == nil {
		t.Fatal("symlink profile accepted")
	}
}
