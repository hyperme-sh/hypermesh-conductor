package proxy

import (
	"fmt"
	"net/http"
	"reflect"
	"sync"
	"sync/atomic"

	"github.com/hyperme-sh/hypermesh-conductor/internal/config"
)

type recipeRuntime struct {
	path   string
	active atomic.Pointer[Proxy]
	mu     sync.Mutex
}

// EnableRecipeReload preserves an immutable configuration snapshot per request.
// Listener, endpoint and policy changes require a process restart.
func (p *Proxy) EnableRecipeReload(path string) {
	child := New(p.Config, p.Hop, p.Journal)
	child.PeerFactory = p.PeerFactory
	child.PeerIngress = p.PeerIngress
	child.Notes = p.Notes
	child.Logf = p.Logf
	runtime := &recipeRuntime{path: path}
	runtime.active.Store(child)
	p.recipes = runtime
}

func withoutRecipe(c config.Config) config.Config {
	for _, tier := range []*config.Tier{&c.Cheap, &c.Frontier} {
		tier.RecipeDigest = ""
		tier.OutputLimit = 0
		tier.TaskTokenLimit = 0
		tier.TaskOutputLimit = 0
		tier.ContextWindow = 0
	}
	return c
}

func (r *recipeRuntime) reload() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	next, err := config.Load(r.path)
	if err != nil {
		return err
	}
	old := r.active.Load()
	next.Journal = old.Config.Journal
	if !reflect.DeepEqual(withoutRecipe(next), withoutRecipe(old.Config)) {
		return fmt.Errorf("only recipe limits may reload; endpoint or policy changes require restart")
	}
	child := New(next, old.Hop, old.Journal)
	child.PeerFactory = old.PeerFactory
	child.PeerIngress = old.PeerIngress
	if child.PeerFactory != nil {
		child.PeerIngress, err = child.PeerFactory(next)
		if err != nil {
			return err
		}
	}
	child.Notes = old.Notes
	child.Logf = old.Logf
	r.active.Store(child)
	return nil
}

func (p *Proxy) serveRuntime(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/config" && r.URL.Path != "/v1/config/reload" {
		p.recipes.active.Load().ServeHTTP(w, r)
		return
	}
	if err := authorizeProxy(r); err != nil {
		writeJSON(w, 401, map[string]any{"error": "proxy authorization required"})
		return
	}
	if r.URL.Path == "/v1/config/reload" {
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		if err := p.recipes.reload(); err != nil {
			writeJSON(w, 400, map[string]any{"error": "recipe reload rejected; previous revision retained"})
			return
		}
	} else if r.Method != "GET" {
		w.WriteHeader(405)
		return
	}
	cfg := p.recipes.active.Load().Config
	result := map[string]any{}
	for _, tier := range []config.Tier{cfg.Cheap, cfg.Frontier} {
		if tier.Configured() {
			result[tier.Name] = map[string]any{"model": tier.Model, "recipe_sha256": tier.RecipeDigest, "max_output_tokens": tier.OutputLimit,
				"max_task_tokens": tier.TaskTokenLimit, "max_task_output_tokens": tier.TaskOutputLimit, "context_window_tokens": tier.ContextWindow}
		}
	}
	writeJSON(w, 200, map[string]any{"schema": "hypermesh.conductor.effective.v1", "tiers": result})
}
