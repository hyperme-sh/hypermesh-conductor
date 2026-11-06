package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hyperme-sh/hypermesh-conductor/internal/metrics"
)

func TestRouteCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Main([]string{"route", "--config", filepath.Join("..", "..", "examples", "endpoints.json"), "--class", "index"}, &stdout, &stderr)
	if code != 0 {
		t.Fatal(code, stderr.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatal(err, stdout.String())
	}
	if payload["job_class"] != "index" || payload["tier"] != "cheap" || payload["model_id"] != "local-small" {
		t.Fatal(payload)
	}
	if payload["prefix_sha256"] == "" {
		t.Fatal("missing prefix hash")
	}
	stdout.Reset()
	stderr.Reset()
	code = Main([]string{"route", "--config", filepath.Join("..", "..", "examples", "endpoints.json"), "--class", "classify", "--suggest", "frontier"}, &stdout, &stderr)
	if code != 0 {
		t.Fatal(stderr.String())
	}
	payload = map[string]any{}
	_ = json.Unmarshal(stdout.Bytes(), &payload)
	if payload["tier"] != "cheap" || payload["suggestion_accepted"] != false {
		t.Fatal(payload)
	}
	reason, _ := payload["reason"].(string)
	if len(reason) < len("rejected out-of-policy upgrade") || reason[:len("rejected out-of-policy upgrade")] != "rejected out-of-policy upgrade" {
		t.Fatal(reason)
	}
}

func TestSummaryCommand(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "journal.jsonl")
	journal, err := metrics.OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	in, out, cache, latency := 10, 2, 4, 3
	if err := journal.Append(metrics.StepRecord{
		TaskID: "task-1", StepID: "step-1", JobClass: "parse", Tier: "cheap", ModelID: "local-small",
		Reason: "policy tier for parse is cheap", InputTokens: &in, OutputTokens: &out, CacheHitTokens: &cache,
		LatencyMS: &latency, PrefixSHA256: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		UsageStatus: metrics.StatusMeasured,
	}); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := Main([]string{"summary", "--journal", path}, &stdout, &stderr); code != 0 {
		t.Fatal(code, stderr.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	totals := payload["totals"].(map[string]any)
	if totals["input_tokens"].(float64) != 10 || totals["cache_hit_rate"].(float64) != 0.4 {
		t.Fatal(totals)
	}
	byTier := totals["by_tier"].(map[string]any)
	if byTier["frontier"].(map[string]any)["steps"].(float64) != 0 {
		t.Fatal(byTier)
	}
}

func TestRunStopsBeforeTheProviderWhenTheJournalDirectoryIsMissing(t *testing.T) {
	hits := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "endpoints.json")
	raw := fmt.Sprintf(`{
		"listen": "127.0.0.1:8787",
		"tiers": {
			"cheap": {"provider":"openai-compatible","base_url":%q,"model":"local-small","auth":"passthrough","timeout_seconds":1},
			"frontier": {"provider":"openai-compatible","base_url":%q,"model":"frontier","auth":"passthrough","timeout_seconds":1}
		},
		"prefix": {"system":"You are Conductor.","tools":[]},
		"aiuditor": {"mode":"stub","fail_closed":true}
	}`, upstream.URL+"/v1", upstream.URL+"/v1")
	if err := os.WriteFile(configPath, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Main([]string{
		"run", "--config", configPath,
		"--journal", filepath.Join(dir, "missing", "journal.jsonl"),
		"--class", "parse", "--task", "task-1", "--step", "step-1", "--message", "hello",
	}, &stdout, &stderr)
	if code != 2 || hits != 0 || !strings.Contains(stderr.String(), "journal directory does not exist") {
		t.Fatalf("code %d hits %d stderr %s", code, hits, stderr.String())
	}
}

func TestHelpHasNoKeyFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Main([]string{"--help"}, &stdout, &stderr); code != 0 {
		t.Fatal(code)
	}
	if bytes.Contains(stdout.Bytes(), []byte("api-key")) {
		t.Fatal(stdout.String())
	}
}
