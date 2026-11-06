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

func TestRouteUsesActivePolicy(t *testing.T) {
	dir := t.TempDir()
	rulesPath := filepath.Join(dir, "active.json")
	if err := os.WriteFile(rulesPath, []byte(`{"schema":"hypermesh.conductor.rules.v0","routing":{"class_tiers":{"parse":"frontier"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "endpoints.json")
	raw := fmt.Sprintf(`{
		"listen": "127.0.0.1:8787",
		"rules_file": %q,
		"tiers": {
			"cheap": {"provider":"openai-compatible","base_url":"http://127.0.0.1:9/v1","model":"local-small","auth":"passthrough"},
			"frontier": {"provider":"openai-compatible","base_url":"http://127.0.0.1:9/v1","model":"frontier","auth":"passthrough"}
		},
		"prefix": {"system":"You are Conductor.","tools":[]}
	}`, rulesPath)
	if err := os.WriteFile(configPath, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := Main([]string{"route", "--config", configPath, "--class", "parse"}, &stdout, &stderr); code != 0 {
		t.Fatal(code, stderr.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["tier"] != "frontier" || payload["model_id"] != "frontier" {
		t.Fatal(payload)
	}
}

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

func TestRunWithoutIDsPrintsTheGeneratedRow(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`))
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
		"run", "--config", configPath, "--journal", filepath.Join(dir, "journal.jsonl"),
		"--class", "parse", "--message", "hello",
	}, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), `"task_id"`) || strings.Contains(stderr.String(), "journal was not written") {
		t.Fatalf("code %d stderr %s stdout %s", code, stderr.String(), stdout.String())
	}
}

func TestJournalRecordIsTheCurrentRun(t *testing.T) {
	records := []metrics.StepRecord{
		{TaskID: "task-1", StepID: "step-1", Tier: "cheap"},
		{TaskID: "task-2", StepID: "step-9", Tier: "frontier"},
	}
	got, ok := journalRecord(records, "task-1", "step-1")
	if !ok || got.TaskID != "task-1" || got.StepID != "step-1" || got.Tier != "cheap" {
		t.Fatal(got, ok)
	}
	if _, ok := journalRecord(records, "task-1", "missing"); ok {
		t.Fatal("missing row was selected")
	}
}

func TestRunEmbeddingAliasUsesTheEmbeddingsEndpoint(t *testing.T) {
	var path string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1]}],"usage":{"prompt_tokens":1,"total_tokens":1}}`))
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
		"run", "--config", configPath, "--journal", filepath.Join(dir, "journal.jsonl"),
		"--class", "embedding", "--task", "task-1", "--step", "step-1", "--input", "test",
	}, &stdout, &stderr)
	if code != 0 || path != "/v1/embeddings" || strings.Contains(stderr.String(), "a chat step needs a message") {
		t.Fatalf("code %d path %s stderr %s stdout %s", code, path, stderr.String(), stdout.String())
	}
}

func TestRunRefusesEnvKeyWithoutProxyToken(t *testing.T) {
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
			"cheap": {"provider":"openai-compatible","base_url":%q,"model":"local-small","auth":"env","api_key_env":"CONDUCTOR_CHEAP_API_KEY","timeout_seconds":1},
			"frontier": {"provider":"openai-compatible","base_url":%q,"model":"frontier","auth":"passthrough","timeout_seconds":1}
		},
		"prefix": {"system":"You are Conductor.","tools":[]},
		"aiuditor": {"mode":"stub","fail_closed":true}
	}`, upstream.URL+"/v1", upstream.URL+"/v1")
	if err := os.WriteFile(configPath, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONDUCTOR_CHEAP_API_KEY", "test-key-do-not-log")
	t.Setenv("CONDUCTOR_PROXY_TOKEN", "")
	var stdout, stderr bytes.Buffer
	code := Main([]string{
		"run", "--config", configPath, "--journal", filepath.Join(dir, "journal.jsonl"),
		"--class", "parse", "--task", "task-1", "--step", "step-1", "--message", "hello",
	}, &stdout, &stderr)
	if code != 2 || hits != 0 || !strings.Contains(stderr.String(), "CONDUCTOR_PROXY_TOKEN") {
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

func TestDreamNotesDiffKeepsExistingLinks(t *testing.T) {
	dir := t.TempDir()
	notesDir := filepath.Join(dir, "notes")
	if err := os.MkdirAll(notesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	alpha := []byte(`---
id: alpha
type: note
status: open
---
# Alpha
Mentions beta and gamma.
[[beta]]
`)
	if err := os.WriteFile(filepath.Join(notesDir, "alpha.md"), alpha, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(notesDir, "beta.md"), []byte(`---
id: beta
type: note
status: open
---
# Beta
Beta detail.
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(notesDir, "gamma.md"), []byte(`---
id: gamma
type: note
status: open
---
# Gamma
Gamma detail.
`), 0o644); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(dir, "journal.jsonl")
	if err := os.WriteFile(journal, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	proposals := filepath.Join(dir, "proposals")
	var stdout, stderr bytes.Buffer
	code := Main([]string{"dream", "--journal", journal, "--proposals", proposals, "--notes", notesDir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code %d stderr %s stdout %s", code, stderr.String(), stdout.String())
	}
	diff, err := os.ReadFile(filepath.Join(proposals, "0001.notes.diff"))
	if err != nil {
		t.Fatal(err, stdout.String())
	}
	text := string(diff)
	if strings.Contains(text, "[[beta]]") || !strings.Contains(text, "[[gamma]]") {
		t.Fatal(text)
	}
	got, err := os.ReadFile(filepath.Join(notesDir, "alpha.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(alpha) {
		t.Fatal("dream applied the note diff")
	}
}
