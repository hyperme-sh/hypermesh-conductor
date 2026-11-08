package proxy

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/hyperme-sh/hypermesh-conductor/internal/config"
)

func TestConcurrentOutputReservationsSurviveRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.sqlite")
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			db, err := reserveTokens(path, "task", fmt.Sprint(i), "revision", tokenBudget{outputLimit: 10, output: 3})
			if db != nil {
				db.Close()
			}
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		} else if !errors.Is(err, errTaskBudget) {
			t.Fatal(err)
		}
	}
	if accepted != 3 {
		t.Fatal(accepted)
	}
	if db, err := reserveTokens(path, "task", "new", "other revision", tokenBudget{outputLimit: 20, output: 1}); db != nil || !errors.Is(err, errTaskBudget) {
		t.Fatal(err)
	}
	db, err := reserveTokens(path, "task", "last", "revision", tokenBudget{outputLimit: 10, output: 1})
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
}

func TestTaskOutputBudgetDispatch(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 2 {
			http.Error(w, "interrupted", 502)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`)
	}))
	defer upstream.Close()
	cfg := loadTestConfig(t, dir, upstream.URL+"/v1", upstream.URL+"/v1", config.AuthPassthrough, config.ProviderOpenAICompatible)
	for _, tier := range []*config.Tier{&cfg.Cheap, &cfg.Frontier} {
		tier.OutputLimit = 8
		tier.TaskOutputLimit = 10
		tier.RecipeDigest = "pinned"
	}
	dispatch := func(task, step string, limit, want int) {
		t.Helper()
		server := New(cfg, nil, openJournal(t, dir))
		req := request("/v1/chat/completions", "code", task, step, "", fmt.Sprintf(`{"messages":[{"role":"user","content":"code"}],"max_tokens":%d}`, limit))
		req.Header.Set("Authorization", "Bearer fixture")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("%s: got %d: %s", step, rec.Code, rec.Body.String())
		}
	}
	dispatch("", "missing", 8, 400)
	dispatch("lease", "one", 8, 200)
	dispatch("lease", "one", 8, 409)
	dispatch("lease", "two", 8, 502)
	dispatch("lease", "three", 1, 409)
	if calls != 2 {
		t.Fatal(calls)
	}
}

func TestTotalBudgetReconcilesAcrossProxyRestarts(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	countBody := `{"count":10,"max_model_len":256}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/tokenize" {
			fmt.Fprint(w, countBody)
			return
		}
		calls++
		if calls == 2 {
			http.Error(w, "interrupted", 502)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":10,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":4}}}`)
	}))
	defer upstream.Close()
	cfg := loadTestConfig(t, dir, upstream.URL+"/v1", upstream.URL+"/v1", config.AuthPassthrough, config.ProviderOpenAICompatible)
	for _, tier := range []*config.Tier{&cfg.Cheap, &cfg.Frontier} {
		tier.InputCounter = "vllm"
		tier.TaskTokenLimit = 30
		tier.OutputLimit = 16
		tier.RecipeDigest = "recipe"
	}
	dispatch := func(step string, n, limit, want int) {
		t.Helper()
		server := New(cfg, nil, openJournal(t, dir))
		req := request("/v1/chat/completions", "code", "lease", step, "", fmt.Sprintf(`{"messages":[{"role":"user","content":"code"}],"n":%d,"max_tokens":%d}`, n, limit))
		req.Header.Set("Authorization", "Bearer fixture")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("%s returned %d: %s", step, rec.Code, rec.Body.String())
		}
	}
	dispatch("a", 2, 6, 409)  // 2 * (10 + 6) exceeds 30
	dispatch("a", 1, 16, 200) // reserve 26, reconcile to 12 including cache
	dispatch("a", 1, 1, 409)  // replay
	countBody = `{}`
	dispatch("b", 1, 8, 503) // no count, no generation or reservation
	countBody = `{"count":10,"max_model_len":256}`
	dispatch("b", 1, 8, 502) // reserve remaining 18; unknown outcome
	dispatch("c", 1, 1, 409)
	if calls != 2 {
		t.Fatal(calls)
	}
}

func TestUpgradeOutputLedgerKeepsPriorCharges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE output_attempts(task TEXT,step TEXT,recipe TEXT,charge INTEGER,PRIMARY KEY(task,step)) WITHOUT ROWID;
 INSERT INTO output_attempts VALUES ('task','old','revision',7)`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := reserveTokens(path, "task", "new", "revision", tokenBudget{outputLimit: 10, output: 4})
	if ledger != nil || !errors.Is(err, errTaskBudget) {
		t.Fatal(err)
	}
	ledger, err = reserveTokens(path, "task", "new", "revision", tokenBudget{outputLimit: 10, output: 3})
	if err != nil {
		t.Fatal(err)
	}
	ledger.Close()
}
