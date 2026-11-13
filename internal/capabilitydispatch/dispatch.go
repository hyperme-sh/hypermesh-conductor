// Package capabilitydispatch requires both machine review and a trusted local
// controller reservation before a tool can run. Notes never grant authority.
package capabilitydispatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/hyperme-sh/hypermesh-conductor/internal/notes"
)

var Refused = errors.New("capability dispatch refused")
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Call struct {
	Scope     string `json:"scope"`
	Owner     string `json:"owner"`
	ID        string `json:"capability_id"`
	Revision  string `json:"revision"`
	Source    string `json:"source_sha256"`
	Graph     string `json:"graph_sha256"`
	CallID    string `json:"call_id"`
	Arguments string `json:"arguments_json"`
}

func (c Call) note() notes.CapabilityCall {
	return notes.CapabilityCall{Scope: c.Scope, Owner: c.Owner, ID: c.ID, Revision: c.Revision, SourceHash: c.Source, GraphHash: c.Graph, Arguments: json.RawMessage(c.Arguments)}
}

type Preparation struct {
	Call         Call                         `json:"call"`
	Requirements notes.CapabilityRequirements `json:"requirements"`
}
type Reservation struct {
	Protocol   int                   `json:"protocol_version"`
	CallID     string                `json:"call_id"`
	CallHash   string                `json:"call_sha256"`
	Permit     string                `json:"permit"`
	ObservedMS int64                 `json:"observed_at_ms"`
	ExpiresMS  int64                 `json:"expires_at_ms"`
	Conditions []string              `json:"conditions"`
	Limits     []notes.CapabilityRef `json:"limits"`
	Locks      []string              `json:"locks"`
}
type Decision struct {
	Protocol int    `json:"protocol_version"`
	CallID   string `json:"call_id"`
	CallHash string `json:"call_sha256"`
	Decision string `json:"decision"`
	Expires  int64  `json:"expires_at"`
}
type Execution struct {
	Call       Call   `json:"call"`
	CallHash   string `json:"call_sha256"`
	Permit     string `json:"permit"`
	DeadlineMS int64  `json:"deadline_ms"`
}
type Result struct {
	Protocol  int    `json:"protocol_version"`
	CallID    string `json:"call_id"`
	CallHash  string `json:"call_sha256"`
	Completed bool   `json:"completed"`
	Output    string `json:"output_json"`
}

type Controller interface {
	Prepare(context.Context, Preparation) (Reservation, error)
	Execute(context.Context, Execution) (Result, error)
	Cancel(context.Context, string, string) error
}
type Reviewer interface {
	Review(context.Context, []byte) (Decision, error)
}
type Journal interface{ Reserve(string, string) error }

type Dispatcher struct {
	Catalog    *notes.CapabilityCatalog
	Controller Controller
	Reviewer   Reviewer
	Journal    Journal
	Now        func() time.Time
}

type authority struct {
	ctx         context.Context
	call        []byte
	hash        string
	id          string
	reviewer    Reviewer
	reservation Reservation
	decision    Decision
}

func (a *authority) Authorize(notes.CapabilityCall) error {
	decision, err := a.reviewer.Review(a.ctx, a.call)
	if err != nil || decision.Protocol != 1 || decision.CallID != a.id || decision.CallHash != a.hash || decision.Decision != "allow" {
		return Refused
	}
	a.decision = decision
	return nil
}
func (a *authority) CheckPrecondition(_ string, condition string) error {
	if !slices.Contains(a.reservation.Conditions, condition) {
		return Refused
	}
	return nil
}
func (a *authority) CheckLimit(_ string, limit notes.CapabilityRef) error {
	if !slices.Contains(a.reservation.Limits, limit) {
		return Refused
	}
	return nil
}

func (d Dispatcher) Dispatch(ctx context.Context, call Call) (Result, error) {
	now := time.Now
	if d.Now != nil {
		now = d.Now
	}
	if d.Catalog == nil || d.Controller == nil || d.Reviewer == nil || d.Journal == nil || len(call.Arguments) > 65536 {
		return Result{}, Refused
	}
	id, err := uuid.Parse(call.CallID)
	if err != nil || id.String() != call.CallID {
		return Result{}, Refused
	}
	requirements, err := d.Catalog.Requirements(call.note())
	if err != nil {
		return Result{}, Refused
	}
	raw, err := json.Marshal(call)
	if err != nil {
		return Result{}, Refused
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	// A durable reservation is made even for a failed attempt. Never replay an
	// uncertain hardware operation after timeout, process restart or lost reply.
	if d.Journal.Reserve(call.CallID, hash) != nil {
		return Result{}, Refused
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	reservation, err := d.Controller.Prepare(ctx, Preparation{call, requirements})
	if err != nil {
		return Result{}, Refused
	}
	// Only a fully bound permit is used for cleanup; never send a caller's permit.
	if reservation.Protocol != 1 || reservation.CallID != call.CallID || reservation.CallHash != hash || !digestPattern.MatchString(reservation.Permit) {
		return Result{}, Refused
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		_ = d.Controller.Cancel(cleanup, call.CallID, reservation.Permit)
	}()
	current := now()
	if reservation.ExpiresMS <= current.UnixMilli() || reservation.ExpiresMS > current.Add(30*time.Second).UnixMilli() || !sameStrings(requirements.Locks, reservation.Locks) {
		return Result{}, Refused
	}
	auth := &authority{ctx: ctx, call: raw, hash: hash, id: call.CallID, reviewer: d.Reviewer, reservation: reservation}
	plan, err := d.Catalog.CheckCall(call.note(), time.UnixMilli(reservation.ObservedMS), current, auth)
	if err != nil {
		return Result{}, Refused
	}
	// Machine grant, controller reservation and note deadline all apply. Recheck
	// after review; time spent waiting for policy must not extend any of them.
	deadline := plan.Deadline
	for _, bound := range []time.Time{time.UnixMilli(reservation.ExpiresMS), time.Unix(auth.decision.Expires, 0)} {
		if bound.Before(deadline) {
			deadline = bound
		}
	}
	if now().Sub(time.UnixMilli(reservation.ObservedMS)) > time.Duration(requirements.FreshnessMS)*time.Millisecond || !now().Before(deadline) || auth.decision.Expires > now().Unix()+30 || ctx.Err() != nil {
		return Result{}, Refused
	}
	executeCtx, stop := context.WithDeadline(ctx, deadline)
	defer stop()
	result, err := d.Controller.Execute(executeCtx, Execution{call, hash, reservation.Permit, deadline.UnixMilli()})
	if err != nil || executeCtx.Err() != nil || !now().Before(deadline) || result.Protocol != 1 || result.CallID != call.CallID || result.CallHash != hash || !result.Completed || len(result.Output) > 1048576 || !json.Valid([]byte(result.Output)) {
		return Result{}, Refused
	}
	return result, nil
}
func sameStrings(left, right []string) bool {
	a := append([]string{}, left...)
	b := append([]string{}, right...)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}
