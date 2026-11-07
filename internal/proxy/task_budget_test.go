package proxy

import (
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
			db, err := reserveOutput(path, "task", fmt.Sprint(i), "revision", 10, 3)
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
	if db, err := reserveOutput(path, "task", "new", "other revision", 20, 1); db != nil || !errors.Is(err, errTaskBudget) {
		t.Fatal(err)
	}
	db, err := reserveOutput(path, "task", "last", "revision", 10, 1)
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
