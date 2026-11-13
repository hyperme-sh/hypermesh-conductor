package capabilitydispatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hyperme-sh/hypermesh-conductor/internal/notes"
)

type memoryJournal struct{ seen map[string]bool }

func (m *memoryJournal) Reserve(id, hash string) error {
	if m.seen[id] {
		return Refused
	}
	m.seen[id] = true
	return nil
}

type fakeReview struct {
	allow bool
	calls int
	after func()
}

func (r *fakeReview) Review(ctx context.Context, raw []byte) (Decision, error) {
	r.calls++
	var call Call
	json.Unmarshal(raw, &call)
	sum := sha256.Sum256(raw)
	decision := "allow"
	if !r.allow {
		decision = "deny"
	}
	result := Decision{1, call.CallID, hex.EncodeToString(sum[:]), decision, time.Now().Unix() + 20}
	if r.after != nil {
		r.after()
	}
	return result, nil
}

type fakeController struct {
	prepares, executes, cancels int
	binding                     Call
	reservation                 Reservation
	prepareChange               func(*Reservation)
	executionError              bool
	check                       func(Execution)
}

func (c *fakeController) Prepare(ctx context.Context, request Preparation) (Reservation, error) {
	c.prepares++
	c.binding = request.Call
	raw, _ := json.Marshal(request.Call)
	sum := sha256.Sum256(raw)
	c.reservation = Reservation{1, request.Call.CallID, hex.EncodeToString(sum[:]), strings.Repeat("a", 64), time.Now().UnixMilli(), time.Now().Add(20 * time.Second).UnixMilli(), request.Requirements.Conditions, request.Requirements.Limits, request.Requirements.Locks}
	if c.prepareChange != nil {
		c.prepareChange(&c.reservation)
	}
	return c.reservation, nil
}
func (c *fakeController) Execute(ctx context.Context, request Execution) (Result, error) {
	c.executes++
	if c.check != nil {
		c.check(request)
	}
	if c.executionError {
		return Result{}, errors.New("private controller error")
	}
	return Result{1, request.Call.CallID, request.CallHash, true, `{"count":7}`}, nil
}
func (c *fakeController) Cancel(context.Context, string, string) error { c.cancels++; return nil }

func dispatchFixture(t *testing.T) (Dispatcher, Call, *fakeController, *fakeReview) {
	t.Helper()
	dir := t.TempDir()
	cache := t.TempDir()
	files, err := filepath.Glob(filepath.Join("..", "..", "docs", "examples", "capability-hosts", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, filepath.Base(path)), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	store, err := notes.OpenCollection(dir, filepath.Join(cache, "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	catalog, err := store.SyncCapabilities(dir)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := catalog.Snapshot("org/example/site/lab")
	if err != nil {
		t.Fatal(err)
	}
	var call Call
	for _, entry := range entries {
		if entry.Definition.ID == "verb/summarize-counters" {
			d := entry.Definition
			call = Call{d.Scope, d.Owner, d.ID, d.Revision, entry.SourceHash, entry.GraphHash, "11111111-1111-4111-8111-111111111111", `{ "window_seconds": 10 }`}
		}
	}
	if call.ID == "" {
		t.Fatal("missing fixture")
	}
	controller := &fakeController{}
	review := &fakeReview{allow: true}
	return Dispatcher{Catalog: catalog, Controller: controller, Reviewer: review, Journal: &memoryJournal{map[string]bool{}}}, call, controller, review
}
func TestDispatchRequiresControllerAndPolicyAndPreservesArguments(t *testing.T) {
	dispatcher, call, controller, review := dispatchFixture(t)
	controller.check = func(execution Execution) {
		if execution.Call.Arguments != call.Arguments || execution.Call != call || execution.Permit != controller.reservation.Permit || execution.DeadlineMS > controller.reservation.ExpiresMS {
			t.Fatal("execution binding changed")
		}
	}
	result, err := dispatcher.Dispatch(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Completed || controller.prepares != 1 || controller.executes != 1 || controller.cancels != 1 || review.calls != 1 {
		t.Fatal("missing dispatch phase")
	}
	if _, err = dispatcher.Dispatch(context.Background(), call); err == nil || controller.prepares != 1 {
		t.Fatal("call replayed")
	}
}
func TestDenyNeverExecutesAndReleasesReservation(t *testing.T) {
	dispatcher, call, controller, review := dispatchFixture(t)
	review.allow = false
	if _, err := dispatcher.Dispatch(context.Background(), call); err == nil || controller.executes != 0 || controller.cancels != 1 {
		t.Fatal("deny reached controller execution")
	}
}
func TestMissingControllerStateAndChangedCatalogRefused(t *testing.T) {
	for _, mode := range []string{"condition", "limit", "lock", "stale", "future", "expired", "graph", "arguments"} {
		t.Run(mode, func(t *testing.T) {
			dispatcher, call, controller, _ := dispatchFixture(t)
			switch mode {
			case "graph":
				call.Graph = strings.Repeat("0", 64)
			case "arguments":
				call.Arguments = `{"window_seconds":61}`
			default:
				controller.prepareChange = func(r *Reservation) {
					switch mode {
					case "condition":
						r.Conditions = nil
					case "limit":
						r.Limits = nil
					case "lock":
						r.Locks = nil
					case "stale":
						r.ObservedMS = time.Now().Add(-time.Hour).UnixMilli()
					case "future":
						r.ObservedMS = time.Now().Add(time.Hour).UnixMilli()
					case "expired":
						r.ExpiresMS = 1
					}
				}
			}
			if _, err := dispatcher.Dispatch(context.Background(), call); err == nil || controller.executes != 0 {
				t.Fatal("unsafe call executed")
			}
		})
	}
}
func TestStateAgesDuringReviewAndUncertainExecutionCannotRetry(t *testing.T) {
	dispatcher, call, controller, review := dispatchFixture(t)
	offset := time.Duration(0)
	dispatcher.Now = func() time.Time { return time.Now().Add(offset) }
	review.after = func() { offset = 2 * time.Second }
	if _, err := dispatcher.Dispatch(context.Background(), call); err == nil || controller.executes != 0 || controller.cancels != 1 {
		t.Fatal("stale state executed")
	}
	dispatcher, call, controller, _ = dispatchFixture(t)
	controller.executionError = true
	if _, err := dispatcher.Dispatch(context.Background(), call); err == nil {
		t.Fatal("controller failure accepted")
	}
	if _, err := dispatcher.Dispatch(context.Background(), call); err == nil || controller.executes != 1 {
		t.Fatal("uncertain call replayed")
	}
}
