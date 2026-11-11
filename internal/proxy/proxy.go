// Package proxy is the localhost OpenAI-compatible and Anthropic-compatible front door.
//
// Each request is classified, reviewed by the AIuditor hop, then forwarded to
// the tier destination. Journals record metrics only.
package proxy

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/hyperme-sh/hypermesh-conductor/internal/aiuditor"
	"github.com/hyperme-sh/hypermesh-conductor/internal/compact"
	"github.com/hyperme-sh/hypermesh-conductor/internal/config"
	"github.com/hyperme-sh/hypermesh-conductor/internal/jsonbody"
	"github.com/hyperme-sh/hypermesh-conductor/internal/metrics"
	"github.com/hyperme-sh/hypermesh-conductor/internal/notes"
	"github.com/hyperme-sh/hypermesh-conductor/internal/prefix"
	"github.com/hyperme-sh/hypermesh-conductor/internal/routing"
	"github.com/hyperme-sh/hypermesh-conductor/internal/rules"
	"github.com/hyperme-sh/hypermesh-conductor/internal/translate"
	"github.com/hyperme-sh/hypermesh-conductor/internal/usage"
)

const maxBody = 32 << 20

// bodyLimit is the total request-body bytes buffered at once, before
// authorization. It is one max body, not one per connection.
var (
	bodyMu    sync.Mutex
	bodyUsed  int
	bodyLimit = maxBody
)

var (
	errBodyTooLarge     = errors.New("request body is too large")
	errBodyBudget       = errors.New("request body budget exceeded")
	errResponseTooLarge = errors.New("provider response is too large")
)

// maxSSELine bounds one streaming event. A provider that never sends a
// newline cannot grow the proxy without limit. Streaming reads have no
// overall deadline, so the cap is what stops the read.
var maxSSELine = maxBody

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,199}$`)

// Proxy serves the local endpoints.
type Proxy struct {
	PeerIngress http.Handler
	Config      config.Config
	Hop         aiuditor.Hop
	Journal     *metrics.Journal
	Notes       *notes.Service
	Logf        func(string, ...any)

	transportOnce sync.Once
	transport     *http.Transport
}

// New builds a proxy. A nil hop is replaced with an allow-all stub.
func New(cfg config.Config, hop aiuditor.Hop, journal *metrics.Journal) *Proxy {
	if hop == nil {
		hop = aiuditor.Stub{}
	}
	return &Proxy{Config: cfg, Hop: hop, Journal: journal, Logf: func(string, ...any) {}}
}

const (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 60 * time.Second
	idleTimeout       = 120 * time.Second
	writeIdle         = 30 * time.Second
	maxConns          = 128
	// Network mode keeps unauthenticated connections on a small pool with
	// short timeouts. Authenticated connections use maxConns.
	maxPreAuthConns      = 16
	networkHeaderTimeout = 5 * time.Second
	networkIdleTimeout   = 5 * time.Second
)

// ListenAndServe binds the configured address. Loopback is the default.
// listen_mode network terminates TLS and refuses to start without a certificate,
// a key, and CONDUCTOR_PROXY_TOKEN.
func (p *Proxy) ListenAndServe() error {
	if err := p.Config.RequireListen(); err != nil {
		return err
	}
	if p.Config.ListenMode != config.ListenModeNetwork {
		if err := config.ValidateListen(p.Config.Listen); err != nil {
			return err
		}
	}
	listener, err := net.Listen("tcp", p.Config.Listen)
	if err != nil {
		return err
	}
	return p.ServeListener(listener)
}

// ServeLocalListener serves a socket created in the owner's private directory.
func (p *Proxy) ServeLocalListener(listener net.Listener) error {
	if _, ok := listener.Addr().(*net.UnixAddr); !ok {
		return fmt.Errorf("local listener must be a Unix socket")
	}
	return p.server().Serve(limitConns(listener, maxConns))
}

// ServeLocalListenerContext closes the socket on shutdown, removing its path.
func (p *Proxy) ServeLocalListenerContext(ctx context.Context, listener net.Listener) error {
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			listener.Close()
		case <-done:
		}
	}()
	return p.ServeLocalListener(listener)
}

// ServeListener applies the loopback policy, or TLS for listen_mode network,
// and the connection limit to an inherited listener.
func (p *Proxy) ServeListener(listener net.Listener) error {
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok || addr.IP == nil {
		return fmt.Errorf("listener must be a TCP socket")
	}
	if p.Config.ListenMode == config.ListenModeNetwork {
		if err := p.Config.RequireListen(); err != nil {
			return err
		}
		cert, err := tls.LoadX509KeyPair(p.Config.TLS.CertFile, p.Config.TLS.KeyFile)
		if err != nil {
			return fmt.Errorf("tls certificate: %w", err)
		}
		// Limit the TCP listener first. Wrapping the TLS listener hides
		// *tls.Conn from net/http, so r.TLS stays nil and a same-origin
		// https Origin is rejected. Unauthenticated connections use a
		// separate, smaller pool than authenticated ones.
		listener = tls.NewListener(newPreAuthListener(listener, maxPreAuthConns, maxConns), &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		})
		return p.server().Serve(listener)
	}
	if !addr.IP.IsLoopback() {
		return fmt.Errorf("listener must be a loopback TCP socket")
	}
	return p.server().Serve(limitConns(listener, maxConns))
}

func (p *Proxy) server() *http.Server {
	header := readHeaderTimeout
	idle := idleTimeout
	srv := &http.Server{
		Handler:     p,
		ReadTimeout: readTimeout,
	}
	if p.Config.ListenMode == config.ListenModeNetwork {
		header = networkHeaderTimeout
		idle = networkIdleTimeout
		srv.ConnContext = func(ctx context.Context, c net.Conn) context.Context {
			if tlsConn, ok := c.(*tls.Conn); ok {
				c = tlsConn.NetConn()
			}
			return context.WithValue(ctx, rawConnKey, c)
		}
	}
	srv.ReadHeaderTimeout = header
	srv.IdleTimeout = idle
	return srv
}

// limitConns stops Accept once n connections are open, so a drip of
// unfinished handshakes cannot create an unbounded number of goroutines.
func limitConns(ln net.Listener, n int) net.Listener {
	return &connLimiter{Listener: ln, slots: make(chan struct{}, n)}
}

type connLimiter struct {
	net.Listener
	slots chan struct{}
}

func (l *connLimiter) Accept() (net.Conn, error) {
	l.slots <- struct{}{}
	conn, err := l.Listener.Accept()
	if err != nil {
		<-l.slots
		return nil, err
	}
	return &trackedConn{Conn: conn, release: func() { <-l.slots }}, nil
}

type trackedConn struct {
	net.Conn
	release func()
	once    sync.Once
}

func (c *trackedConn) Close() error {
	c.once.Do(c.release)
	return c.Conn.Close()
}

type rawConnKeyType struct{}

var rawConnKey rawConnKeyType

// newPreAuthListener admits few connections that have not authenticated.
// A successful proxy-token check promotes the connection onto the larger pool.
func newPreAuthListener(ln net.Listener, preN, authN int) net.Listener {
	return &preAuthListener{Listener: ln, pre: make(chan struct{}, preN), auth: make(chan struct{}, authN)}
}

type preAuthListener struct {
	net.Listener
	pre  chan struct{}
	auth chan struct{}
}

func (l *preAuthListener) Accept() (net.Conn, error) {
	l.pre <- struct{}{}
	conn, err := l.Listener.Accept()
	if err != nil {
		<-l.pre
		return nil, err
	}
	return &preAuthConn{Conn: conn, pre: l.pre, auth: l.auth, holdingPre: true}, nil
}

type preAuthConn struct {
	net.Conn
	pre        chan struct{}
	auth       chan struct{}
	mu         sync.Mutex
	holdingPre bool
	promoted   bool
	closed     bool
}

// Promote moves the connection from the unauthenticated pool to the authenticated pool.
func (c *preAuthConn) Promote() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false
	}
	if c.promoted {
		return true
	}
	select {
	case c.auth <- struct{}{}:
	default:
		return false
	}
	c.promoted = true
	if c.holdingPre {
		<-c.pre
		c.holdingPre = false
	}
	return true
}

func (c *preAuthConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.closed = true
		if c.promoted {
			<-c.auth
		} else if c.holdingPre {
			<-c.pre
			c.holdingPre = false
		}
	}
	return c.Conn.Close()
}

func closeNetworkConn(w http.ResponseWriter) {
	w.Header().Set("Connection", "close")
}

func promoteNetworkConn(w http.ResponseWriter, r *http.Request) bool {
	conn, _ := r.Context().Value(rawConnKey).(*preAuthConn)
	if conn == nil || conn.Promote() {
		return true
	}
	closeNetworkConn(w)
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]any{"message": "connection limit reached"}})
	return false
}

// ServeHTTP implements the local API.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if p.Config.ListenMode == config.ListenModeNetwork {
		if err := authorizeNetwork(r); err != nil {
			closeNetworkConn(w)
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{"message": err.Error()}})
			return
		}
		if !promoteNetworkConn(w, r) {
			return
		}
	}
	// Native API clients omit browser provenance headers. Only same-origin
	// browser requests may reach local providers.
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	origins := r.Header.Values("Origin")
	sites := r.Header.Values("Sec-Fetch-Site")
	if (len(origins) != 0 && (len(origins) != 1 || !strings.EqualFold(origins[0], scheme+"://"+r.Host))) ||
		(len(sites) != 0 && (len(sites) != 1 || (sites[0] != "same-origin" && sites[0] != "none"))) {
		http.Error(w, "cross-origin request denied", http.StatusForbidden)
		return
	}
	w = deadlineWriter{ResponseWriter: w, idle: writeIdle}
	if p.PeerIngress != nil && r.Method == http.MethodPost && r.URL.Path == "/v1/peer/prompt-submit" {
		p.PeerIngress.ServeHTTP(w, r)
		return
	}
	proto, ok := protocolOf(r.Method, r.URL.Path)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"message": "unknown path"}})
		return
	}
	if proto == translate.ProtoModels {
		p.serveModels(w, r)
		return
	}
	body, err := readRequestBody(r.Body)
	held := len(body)
	defer func() { releaseBody(held) }()
	if errors.Is(err, errBodyTooLarge) {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": map[string]any{"message": "request body is too large"}})
		return
	}
	if errors.Is(err, errBodyBudget) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]any{"message": "request body budget exceeded"}})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "unreadable body"}})
		return
	}
	obj, err := jsonbody.Parse(body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "request body must be a JSON object"}})
		return
	}
	originalLease := strings.TrimSpace(rawString(obj.Values["lease_id"]))
	callerTask := firstNonEmpty(r.Header.Get("X-Conductor-Task"), rawString(obj.Values["conductor_task"]))
	className, suggest, taskID, stepID, err := p.identity(r, obj, proto)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": err.Error()}})
		return
	}
	accountingURL := os.Getenv("CONDUCTOR_ACCOUNTING_URL")
	if accountingURL != "" {
		if err := authorizeProxy(r); err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "proxy authorization required"})
			return
		}
	}
	obj.DropPrefix("conductor_")
	p.injectPrefix(&obj, proto)
	prepared, err := obj.Marshal()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "request body must be a JSON object"}})
		return
	}
	if !replaceReservation(held, len(prepared)) {
		held = 0
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]any{"message": "request body budget exceeded"}})
		return
	}
	held = len(prepared)
	body = nil
	policy, err := rules.Load(p.Config.RulesFile)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": err.Error()}})
		return
	}
	peak := compact.EstimateTokens(string(prepared))
	clientHash, _ := hashBody(proto, prepared)
	configHash, _ := p.prefixHash(proto)
	prefixMatch := clientHash != "" && clientHash == configHash
	if suggest == "" {
		suggest = rules.Suggest(policy, prefixMatch)
	}
	decision, err := routing.Route(className, suggest, rules.EffectiveClassTiers(p.Config.ClassTiers, policy, className, peak))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": err.Error()}})
		return
	}
	tier, err := p.Config.Endpoint(decision.Tier)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": err.Error()}})
		return
	}
	if taskIDRequired(accountingURL, tier, proto) && callerTask == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "task budget requires an explicit task ID"}})
		return
	}
	if accountingURL != "" && tier.TaskTokenLimit <= 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "plane accounting requires a task token limit"})
		return
	}
	if expected := r.Header.Values("X-Conductor-Expected-Model"); len(expected) > 0 && (len(expected) != 1 || expected[0] != tier.Model) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": map[string]any{"message": "route does not match expected model"}})
		return
	}
	if tier.Gateway {
		if err := leaseHeaderConflict(r, originalLease); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": err.Error()}})
			return
		}
	}
	stream := boolField(obj, "stream")
	prelimHash, _ := hashBody(proto, prepared)
	base := p.baseRecord(taskID, stepID, decision, tier.Model, prelimHash)
	if prefixMatch {
		yes := true
		base.PrefixCacheMatch = &yes
	}
	prepared, halted, reviewRelease := p.applyReview(w, r, &base, proto, prepared, stream, decision, tier, prelimHash)
	defer reviewRelease()
	if halted {
		return
	}
	if !replaceReservation(held, len(prepared)) {
		held = 0
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]any{"message": "request body budget exceeded"}})
		return
	}
	held = len(prepared)
	if hash, err := hashBody(proto, prepared); err == nil {
		prelimHash = hash
	}
	useNotes := p.Notes != nil && acceptsNotes(proto) && authorizeProxy(r) == nil
	var query string
	if useNotes {
		query = userQuery(prepared)
	}
	next, didCompact, memLookups, memHits, compactErr := p.maybeCompact(r.Context(), r, proto, prepared, taskID, stepID, prelimHash, tier, originalLease)
	if compactErr != nil {
		var denied proxyAuthError
		if errors.As(compactErr, &denied) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{"message": compactErr.Error()}})
			return
		}
		var rejected gatewayRejectedError
		if errors.As(compactErr, &rejected) {
			writeJSON(w, rejected.status, map[string]any{"error": map[string]any{"message": rejected.Error()}})
			return
		}
		if errors.Is(compactErr, errBodyBudget) {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]any{"message": "request body budget exceeded"}})
			return
		}
	}
	if didCompact {
		prepared = next
		yes := true
		base.Compacted = &yes
	}
	beforeNotes := prepared
	if useNotes {
		updated, got, err := p.insertNotes(prepared, query, taskID, stepID, true)
		memLookups += got.Lookups
		memHits += got.Hits
		if err == nil {
			prepared = updated
		}
	}
	if p.Notes != nil && p.Notes.Cache != nil {
		lookups, hits := memLookups, memHits
		base.MemoryLookups = &lookups
		base.MemoryHits = &hits
	}
	if didCompact || !bytes.Equal(beforeNotes, prepared) {
		var halted bool
		var secondRelease func()
		prepared, halted, secondRelease = p.applyReview(w, r, &base, proto, prepared, stream, decision, tier, base.PrefixSHA256)
		defer secondRelease()
		if halted {
			return
		}
	}
	if !replaceReservation(held, len(prepared)) {
		held = 0
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]any{"message": "request body budget exceeded"}})
		return
	}
	held = len(prepared)
	forced, err := forceModel(prepared, tier.Model)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": err.Error()}})
		return
	}
	wire, err := translateTierRequest(proto, tier, forced)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": err.Error()}})
		return
	}
	var allowance generationAllowance
	wire.Body, allowance, err = boundedOutput(wire.Protocol, wire.Body, tier.OutputLimit)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": err.Error()}})
		return
	}
	base.PrefixCacheMatch = nil
	if hash, err := hashBody(wire.Protocol, wire.Body); err == nil {
		base.PrefixSHA256 = hash
		configured, err := p.prefixHash(wire.Protocol)
		if err == nil {
			match := hash == configured
			base.PrefixCacheMatch = &match
		}
	}
	secrets, authReq, err := p.upstreamRequest(r, tier, wire, stream, originalLease)
	if err != nil {
		status := http.StatusBadRequest
		var denied proxyAuthError
		if errors.As(err, &denied) {
			status = http.StatusUnauthorized
		}
		writeJSON(w, status, map[string]any{"error": map[string]any{"message": err.Error()}})
		return
	}
	count, status, countErr := p.checkContext(r, authReq, tier, wire.Protocol, wire.Body, gatewayLease(tier, wire.LeaseID, originalLease, r))
	if countErr != nil {
		writeJSON(w, status, map[string]any{"error": map[string]any{"message": countErr.Error()}})
		return
	}
	base.CountedInputTokens = count
	estimated := compact.EstimateTokens(string(wire.Body))
	base.EstimatedInputTokens = &estimated
	base.RecipeSHA256 = tier.RecipeDigest
	if accountingURL != "" {
		if tier.TaskTokenLimit <= 0 || count == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "plane accounting requires counted input and a task token limit"})
			return
		}
	}
	if tier.TaskTokenLimit > 0 || (tier.TaskOutputLimit > 0 && wire.Protocol != translate.ProtoEmbeddings) {
		if p.Journal == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]any{"message": "task budget requires a durable journal"}})
			return
		}
		inputCharge := 0
		if tier.TaskTokenLimit > 0 {
			if count == nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "task budget requires counted input"}})
				return
			}
			if allowance.candidates < 1 || *count > tier.TaskTokenLimit/allowance.candidates {
				writeJSON(w, http.StatusConflict, map[string]any{"error": map[string]any{"message": "counted input exceeds task token allowance"}})
				return
			}
			inputCharge = *count * allowance.candidates
		}
		attemptID := ""
		if accountingURL != "" {
			attemptID = uuid.NewString()
		}
		ledger, reserveErr := reserveTokens(p.Journal.Path()+".output.sqlite", taskID, stepID, tier.RecipeDigest, tokenBudget{attemptID: attemptID, outputLimit: tier.TaskOutputLimit, totalLimit: tier.TaskTokenLimit, input: inputCharge, output: allowance.output})
		if reserveErr != nil {
			status, message := http.StatusServiceUnavailable, "task budget ledger unavailable"
			if errors.Is(reserveErr, errTaskBudget) {
				status, message = http.StatusConflict, errTaskBudget.Error()
			}
			writeJSON(w, status, map[string]any{"error": map[string]any{"message": message}})
			return
		}
		defer ledger.Close()
		if accountingURL != "" {
			hold := planeReservation{Task: taskID, Step: stepID, Model: tier.Model, Recipe: tier.RecipeDigest, Source: tier.InputCounter, Input: inputCharge, Output: allowance.output}
			if err := registerConfiguredReservation(r.Context(), accountingURL, attemptID, hold); err != nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "plane reservation unconfirmed; attempt remains held"})
				return
			}
			authReq.Header.Set("X-Hypermesh-Token-Attempt", attemptID)
		}
		r = r.WithContext(context.WithValue(r.Context(), budgetContextKey{}, ledger))
	}
	p.forward(w, r, authReq, tier, wire, stream, secrets, base)
}

func (p *Proxy) forward(w http.ResponseWriter, client *http.Request, upstream *http.Request, tier config.Tier, wire translate.Wire, stream bool, secrets []string, record metrics.StepRecord) {
	start := time.Now()
	ctx, cancel := context.WithCancel(upstream.Context())
	defer cancel()
	upstream = upstream.WithContext(ctx)
	headerTimeout := tier.Timeout
	if headerTimeout <= 0 {
		headerTimeout = 60 * time.Second
	}
	headerTimer := time.AfterFunc(headerTimeout, cancel)
	resp, err := p.upstreamClient(tier.Timeout, stream).Do(upstream)
	headerTimer.Stop()
	if err != nil {
		ms := elapsedMS(start)
		record.LatencyMS = &ms
		msg := "provider request failed"
		record.Error = &msg
		record.UsageStatus = metrics.StatusUncertain
		p.finish(w, client, http.StatusBadGateway, nil, record, secrets, false)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		_ = resp.Body.Close()
		ms := elapsedMS(start)
		record.LatencyMS = &ms
		msg := fmt.Sprintf("provider HTTP %d", resp.StatusCode)
		record.Error = &msg
		record.UsageStatus = metrics.StatusUncertain
		p.finish(w, client, http.StatusBadGateway, nil, record, secrets, false)
		return
	}
	if resp.StatusCode >= 400 {
		timeout := tier.Timeout
		if timeout <= 0 {
			timeout = 60 * time.Second
		}
		timer := time.AfterFunc(timeout, func() { _ = resp.Body.Close() })
		defer timer.Stop()
		raw, held, readErr := readReserved(resp.Body, 1<<20)
		defer releaseBody(held)
		if errors.Is(readErr, errBodyBudget) {
			_ = resp.Body.Close()
			ms := elapsedMS(start)
			record.LatencyMS = &ms
			msg := "request body budget exceeded"
			record.Error = &msg
			record.UsageStatus = metrics.StatusUncertain
			p.finish(w, client, http.StatusServiceUnavailable, nil, record, secrets, false)
			return
		}
		ms := elapsedMS(start)
		record.LatencyMS = &ms
		msg := fmt.Sprintf("provider HTTP %d", resp.StatusCode)
		record.Error = &msg
		record.UsageStatus = metrics.StatusUncertain
		p.finish(w, client, resp.StatusCode, raw, record, secrets, false)
		return
	}
	if stream {
		timeout := tier.Timeout
		if timeout <= 0 {
			timeout = 60 * time.Second
		}
		resp.Body = &idleReadBody{ReadCloser: resp.Body, timeout: timeout}
		p.stream(w, client, resp, wire, start, secrets, record)
		return
	}
	raw, held, err := readReserved(resp.Body, maxBody)
	defer releaseBody(held)
	ms := elapsedMS(start)
	record.LatencyMS = &ms
	if errors.Is(err, errBodyBudget) {
		_ = resp.Body.Close()
		msg := "request body budget exceeded"
		record.Error = &msg
		record.UsageStatus = metrics.StatusUncertain
		p.finish(w, client, http.StatusServiceUnavailable, nil, record, secrets, false)
		return
	}
	if err != nil {
		msg := "provider request failed"
		if errors.Is(err, errResponseTooLarge) {
			msg = "provider response is too large"
		}
		record.Error = &msg
		record.UsageStatus = metrics.StatusUncertain
		p.finish(w, client, http.StatusBadGateway, nil, record, secrets, false)
		return
	}
	if providerError(raw) {
		msg := "provider response failed"
		record.Error = &msg
		record.UsageStatus = metrics.StatusUncertain
		p.finish(w, client, http.StatusBadGateway, nil, record, secrets, false)
		return
	}
	converted, err := translate.Response(protocolOfRequest(client), wire.Protocol, raw)
	if err != nil {
		msg := err.Error()
		record.Error = &msg
		record.UsageStatus = metrics.StatusUncertain
		p.finish(w, client, http.StatusBadGateway, nil, record, secrets, false)
		return
	}
	parsed := usage.ParseOpenAI(raw, wire.Protocol == translate.ProtoEmbeddings)
	if parsed.Status != usage.Measured {
		parsed = usage.ParseAnthropic(raw)
	}
	applyUsage(&record, parsed, p.Config, record.Tier)
	p.finish(w, client, http.StatusOK, converted, record, secrets, false)
}

// idleReadBody permits long streams while bounding each stalled upstream read.
type idleReadBody struct {
	io.ReadCloser
	timeout time.Duration
}

func (b *idleReadBody) Read(p []byte) (int, error) {
	timer := time.AfterFunc(b.timeout, func() { _ = b.ReadCloser.Close() })
	defer timer.Stop()
	return b.ReadCloser.Read(p)
}

func (p *Proxy) stream(w http.ResponseWriter, client *http.Request, resp *http.Response, wire translate.Wire, start time.Time, secrets []string, record metrics.StepRecord) {
	stampRun(w, record)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if err := flushResult(w); err != nil {
		_ = resp.Body.Close()
		ms := elapsedMS(start)
		record.LatencyMS = &ms
		msg := "provider request failed"
		record.Error = &msg
		record.UsageStatus = metrics.StatusUncertain
		p.journal(record)
		p.logRecord(client, record)
		return
	}
	bridge := translate.NewStream(protocolOfRequest(client), wire.Protocol)
	defer bridge.Close()
	passthrough := bridge.Passthrough()
	acc := usage.UncertainUsage()
	buf := make([]byte, 32*1024)
	var pending []byte
	var readErr error
	failed := false
	terminal := false
	clientGone := false
	credentialGuard := newStreamCredentials(secrets)
	writeClient := func(p []byte) bool {
		if clientGone || len(p) == 0 {
			return !clientGone
		}
		safe := redactEventData(p, secrets)
		if !credentialGuard.safeEvents(safe) {
			failed = true
			readErr = fmt.Errorf("provider stream contained credential material")
			_ = resp.Body.Close()
			return false
		}
		if _, err := w.Write(safe); err != nil {
			clientGone = true
			readErr = err
			_ = resp.Body.Close()
			return false
		}
		if err := flushResult(w); err != nil {
			clientGone = true
			readErr = err
			_ = resp.Body.Close()
			return false
		}
		return true
	}
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			pending = append(pending, chunk...)
			overflow := false
			var ready []byte
			for {
				i := bytes.IndexByte(pending, '\n')
				if i < 0 {
					if len(pending) > maxSSELine {
						overflow = true
					}
					break
				}
				if i > maxSSELine {
					overflow = true
					break
				}
				line := pending[:i]
				ready = append(ready, pending[:i+1]...)
				pending = pending[i+1:]
				if streamDone(line) {
					terminal = true
				}
				payload := ssePayload(line)
				if payload != nil {
					if providerError(payload) {
						failed = true
						terminal = true
					}
					usage.Absorb(&acc, payload, wire.Protocol == translate.ProtoEmbeddings)
				}
				if terminal {
					// Complete the SSE frame even when the transport split its
					// final blank line into a read that would otherwise block.
					ready = append(ready, '\n')
					pending = nil
					break
				}
			}
			if overflow {
				pending = nil
				readErr = fmt.Errorf("provider stream event is too large")
				_ = resp.Body.Close()
				break
			}
			if passthrough {
				if terminal && !sseEventClosed(ready) {
					ready = append(ready, '\n')
				}
				if !writeClient(ready) {
					break
				}
				if terminal {
					_ = resp.Body.Close()
					break
				}
			} else {
				out := bridge.Consume(chunk)
				if !writeClient(out) {
					break
				}
				if bridge.TooLarge() {
					pending = nil
					readErr = fmt.Errorf("provider stream event is too large")
					_ = resp.Body.Close()
					break
				}
				if bridge.Closed() {
					_ = resp.Body.Close()
					break
				}
			}
		}
		if err != nil {
			if err == io.EOF && !terminal && !bridge.Closed() {
				readErr = fmt.Errorf("provider stream ended before completion")
			} else if err != io.EOF {
				readErr = err
			}
			break
		}
	}
	if len(pending) > 0 && readErr == nil {
		if payload := ssePayload(pending); payload != nil {
			if providerError(payload) {
				failed = true
			}
			usage.Absorb(&acc, payload, wire.Protocol == translate.ProtoEmbeddings)
		}
		if passthrough && !writeClient(pending) {
			pending = nil
		} else if passthrough {
			pending = nil
		}
	}
	if !clientGone {
		var tail []byte
		if readErr != nil {
			tail = bridge.Abort()
		} else {
			tail = bridge.Finish()
		}
		_ = writeClient(tail)
	}
	ms := elapsedMS(start)
	record.LatencyMS = &ms
	if readErr != nil || failed {
		msg := "provider request failed"
		record.Error = &msg
		record.UsageStatus = metrics.StatusUncertain
		record.InputTokens = nil
		record.OutputTokens = nil
		record.CacheHitTokens = nil
		record.CostProxy = nil
		record.CostProxyLabel = nil
	} else {
		applyUsage(&record, acc, p.Config, record.Tier)
	}
	p.recordDispatch(client, record)
}

func (p *Proxy) finish(w http.ResponseWriter, client *http.Request, status int, body []byte, record metrics.StepRecord, secrets []string, streamed bool) {
	stampRun(w, record)
	if !streamed {
		if body == nil {
			body = mustJSON(map[string]any{"error": map[string]any{"message": errorText(record)}})
		}
		body = redactJSON(body, secrets)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}
	p.recordDispatch(client, record)
}

func (p *Proxy) recordDispatch(client *http.Request, record metrics.StepRecord) {
	ledger, _ := client.Context().Value(budgetContextKey{}).(*sql.DB)
	if ledger != nil && record.UsageStatus == metrics.StatusMeasured && record.OutputTokens != nil && *record.OutputTokens >= 0 && record.InputTokens != nil && *record.InputTokens >= 0 {
		if _, err := ledger.Exec("UPDATE output_attempts SET charge=?, input_charge=? WHERE task=? AND step=?", *record.OutputTokens, *record.InputTokens, record.TaskID, record.StepID); err != nil {
			p.Logf("task budget reconciliation failed")
		}
	}
	p.journal(record)
	p.logRecord(client, record)
}

func (p *Proxy) journal(record metrics.StepRecord) {
	if p.Journal == nil {
		return
	}
	if err := p.Journal.Append(record); err != nil && p.Logf != nil {
		p.Logf("journal write failed")
	}
}

func (p *Proxy) logRecord(client *http.Request, record metrics.StepRecord) {
	if p.Logf == nil {
		return
	}
	in, out := "-", "-"
	if record.InputTokens != nil {
		in = fmt.Sprintf("%d", *record.InputTokens)
	}
	if record.OutputTokens != nil {
		out = fmt.Sprintf("%d", *record.OutputTokens)
	}
	p.Logf("path=%s class=%s tier=%s status=%s input_tokens=%s output_tokens=%s", client.URL.Path, record.JobClass, record.Tier, record.UsageStatus, in, out)
}

func (p *Proxy) serveModels(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSpace(os.Getenv(config.ProxyTokenEnv)) != "" {
		if err := authorizeProxy(r); err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{"message": err.Error()}})
			return
		}
	}
	data := make([]any, 0, 2)
	if p.Config.Cheap.Configured() {
		data = append(data, map[string]any{"id": p.Config.Cheap.Model, "object": "model", "owned_by": routing.Cheap})
	}
	if p.Config.Frontier.Configured() {
		data = append(data, map[string]any{"id": p.Config.Frontier.Model, "object": "model", "owned_by": routing.Frontier})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   data,
	})
}

func (p *Proxy) identity(r *http.Request, obj jsonbody.Object, proto string) (className, suggest, taskID, stepID string, err error) {
	className = firstNonEmpty(r.Header.Get("X-Conductor-Class"), rawString(obj.Values["conductor_class"]))
	if className == "" && proto == translate.ProtoEmbeddings {
		className = "embed"
	}
	if className == "" {
		className = p.Config.DefaultClass
	}
	if className == "" {
		fallback, ferr := p.Config.FallbackClass()
		if ferr != nil {
			return "", "", "", "", ferr
		}
		className = fallback
		if p.Logf != nil {
			p.Logf("class fallback=%s reason=no X-Conductor-Class and no default_class", className)
		}
	}
	suggest = firstNonEmpty(r.Header.Get("X-Conductor-Suggest"), rawString(obj.Values["conductor_suggest"]))
	taskID = firstNonEmpty(r.Header.Get("X-Conductor-Task"), rawString(obj.Values["conductor_task"]))
	stepID = firstNonEmpty(r.Header.Get("X-Conductor-Step"), rawString(obj.Values["conductor_step"]))
	if taskID == "" {
		taskID = NewTaskID()
	}
	if stepID == "" {
		stepID = newID("s")
	}
	if !idPattern.MatchString(taskID) || !idPattern.MatchString(stepID) {
		return "", "", "", "", fmt.Errorf("task_id and step_id must be short stable ids")
	}
	return className, suggest, taskID, stepID, nil
}

func (p *Proxy) injectPrefix(obj *jsonbody.Object, proto string) {
	if p.Config.Prefix.System == "" || proto == translate.ProtoEmbeddings {
		return
	}
	switch proto {
	case translate.ProtoMessages:
		if len(obj.Values["system"]) == 0 || string(obj.Values["system"]) == "null" || string(obj.Values["system"]) == `""` {
			_ = obj.Set("system", p.Config.Prefix.System)
		}
	case translate.ProtoResponses:
		if len(obj.Values["instructions"]) == 0 {
			_ = obj.Set("instructions", p.Config.Prefix.System)
		}
	default:
		messages := decodeArray(obj.Values["messages"])
		hasSystem := false
		for _, item := range messages {
			msg, _ := item.(map[string]any)
			if msg["role"] == "system" {
				hasSystem = true
			}
		}
		if !hasSystem {
			messages = append([]any{map[string]any{"role": "system", "content": p.Config.Prefix.System}}, messages...)
			_ = obj.Set("messages", messages)
		}
	}
	if len(obj.Values["tools"]) == 0 && len(p.Config.Prefix.Tools) > 0 && proto != translate.ProtoResponses {
		_ = obj.Set("tools", translate.ClientTools(proto, p.Config.Prefix.Tools))
	}
}

func (p *Proxy) upstreamRequest(client *http.Request, tier config.Tier, wire translate.Wire, stream bool, originalLease string) ([]string, *http.Request, error) {
	if tier.IncludeUsage && stream && wire.Protocol == translate.ProtoChat && tier.Provider == config.ProviderOpenAI {
		obj, err := jsonbody.Parse(wire.Body)
		if err == nil {
			_ = obj.Set("stream_options", map[string]any{"include_usage": true})
			if raw, err := obj.Marshal(); err == nil {
				wire.Body = raw
			}
		}
	}
	url := tier.BaseURL + "/" + wire.Path
	req, err := http.NewRequestWithContext(client.Context(), http.MethodPost, url, bytes.NewReader(wire.Body))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	secrets, err := applyGatewayDispatch(req, client, tier, gatewayLease(tier, wire.LeaseID, originalLease, client))
	if err != nil {
		return nil, nil, err
	}
	if tier.Provider == config.ProviderAnthropic || wire.Protocol == translate.ProtoMessages {
		version := client.Header.Get("anthropic-version")
		if version == "" {
			version = "2023-06-01"
		}
		req.Header.Set("anthropic-version", version)
	}
	return secrets, req, nil
}

func applyAuth(dst, src *http.Request, tier config.Tier) ([]string, error) {
	var secrets []string
	if err := rejectNonRenterCredentials(src); err != nil {
		return nil, err
	}
	clientKey := clientCredential(src, tier.Provider)
	if tier.Gateway {
		key, err := gatewayClientKey(src)
		if err != nil {
			return nil, err
		}
		clientKey = key
	}
	if tier.Auth == config.AuthEnv {
		clientKey = ""
	}
	var envKey string
	var err error
	needEnv := tier.APIKeyEnv != "" && (tier.Auth == config.AuthEnv || (tier.Auth == config.AuthPassthroughOrEnv && clientKey == ""))
	if needEnv {
		if err = authorizeProxy(src); err != nil {
			return nil, err
		}
		envKey, err = tier.Key()
		if err != nil {
			return nil, err
		}
	}
	if isNonRenterCredential(envKey) {
		return nil, fmt.Errorf("refusing to forward a non-renter credential")
	}
	if tier.Gateway {
		if err := applyGatewayIdentity(dst, src, tier, clientKey, needEnv, envKey); err != nil {
			return nil, err
		}
		if key := dst.Header.Get("X-Api-Key"); key != "" {
			secrets = append(secrets, key)
		}
		if header := dst.Header.Get("Authorization"); header != "" {
			secrets = append(secrets, header)
			if len(header) > 7 && strings.EqualFold(header[:7], "bearer ") {
				secrets = append(secrets, strings.TrimSpace(header[7:]))
			}
		}
	} else {
		switch tier.Provider {
		case config.ProviderAnthropic:
			key := clientKey
			if needEnv {
				key = envKey
			}
			if key != "" {
				dst.Header.Set("x-api-key", key)
				secrets = append(secrets, key)
			}
		default:
			key := clientKey
			if needEnv {
				key = envKey
			}
			if key != "" {
				dst.Header.Set("Authorization", "Bearer "+key)
				secrets = append(secrets, key)
			}
		}
	}
	if header := src.Header.Get("Authorization"); header != "" {
		secrets = append(secrets, header)
		if strings.HasPrefix(strings.ToLower(header), "bearer ") {
			secrets = append(secrets, strings.TrimSpace(header[7:]))
		}
	}
	if key := src.Header.Get("x-api-key"); key != "" {
		secrets = append(secrets, key)
	}
	return secrets, nil
}

func clientCredential(src *http.Request, provider string) string {
	if provider == config.ProviderAnthropic {
		return strings.TrimSpace(src.Header.Get("x-api-key"))
	}
	return bearerToken(src)
}

func gatewayClientKey(src *http.Request) (string, error) {
	apiKey := strings.TrimSpace(src.Header.Get("X-Api-Key"))
	bearer := bearerToken(src)
	if apiKey != "" && bearer != "" {
		return "", fmt.Errorf("send Authorization or X-Api-Key, not both")
	}
	if apiKey != "" {
		return apiKey, nil
	}
	return bearer, nil
}

func applyGatewayIdentity(dst, src *http.Request, tier config.Tier, clientKey string, needEnv bool, envKey string) error {
	switch {
	case needEnv:
		dst.Header.Set("Authorization", "Bearer "+envKey)
	case clientKey != "" && strings.TrimSpace(src.Header.Get("X-Api-Key")) != "" && tier.Auth != config.AuthEnv:
		dst.Header.Set("X-Api-Key", clientKey)
	case clientKey != "":
		dst.Header.Set("Authorization", "Bearer "+clientKey)
	}
	copyHeader(dst, src, "X-Tenant-ID")
	return nil
}

// applyGatewayDispatch is the only way a request is prepared for a tier.
// Gateway auth, renter headers, and the reviewed lease are attached together.
// Every JSON POST built here carries Content-Type, including the tokenizer.
func applyGatewayDispatch(dst, src *http.Request, tier config.Tier, lease string) ([]string, error) {
	if dst.Header.Get("Content-Type") == "" {
		dst.Header.Set("Content-Type", "application/json")
	}
	secrets, err := applyAuth(dst, src, tier)
	if err != nil {
		return nil, err
	}
	if !tier.Gateway {
		return secrets, nil
	}
	dst.Header.Del("X-Hypermesh-Lease-Id")
	dst.Header.Del("X-Lease-Id")
	if lease != "" {
		dst.Header.Set("X-Hypermesh-Lease-Id", lease)
	}
	return secrets, nil
}

// gatewayLease is the selector AIuditor left on the body. A removed body
// lease is not restored from a caller header.
func gatewayLease(tier config.Tier, finalLease, originalLease string, src *http.Request) string {
	if !tier.Gateway {
		return ""
	}
	if finalLease != "" {
		return finalLease
	}
	if originalLease != "" {
		return ""
	}
	if src == nil {
		return ""
	}
	return firstNonEmpty(src.Header.Get("X-Hypermesh-Lease-Id"), src.Header.Get("X-Lease-Id"))
}

func bodyLease(body []byte) string {
	obj, err := jsonbody.Parse(body)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(rawString(obj.Values["lease_id"]))
}

func leaseHeaderConflict(r *http.Request, bodyLease string) error {
	if bodyLease == "" {
		return nil
	}
	for _, name := range []string{"X-Hypermesh-Lease-Id", "X-Lease-Id", "X-Conductor-Task"} {
		value := strings.TrimSpace(r.Header.Get(name))
		if value != "" && value != bodyLease {
			return fmt.Errorf("%s conflicts with body lease_id", name)
		}
	}
	return nil
}

func rejectNonRenterCredentials(src *http.Request) error {
	if isNonRenterCredential(bearerToken(src)) || isNonRenterCredential(strings.TrimSpace(src.Header.Get("X-Api-Key"))) {
		return fmt.Errorf("refusing to forward a non-renter credential")
	}
	return nil
}

func isNonRenterCredential(value string) bool {
	value = strings.TrimSpace(value)
	for _, prefix := range []string{"hm_dev_", "hm_rtr_", "hm_site_", "hm_gw_"} {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func bearerToken(src *http.Request) string {
	header := strings.TrimSpace(src.Header.Get("Authorization"))
	if len(header) > 7 && strings.EqualFold(header[:7], "bearer ") {
		return strings.TrimSpace(header[7:])
	}
	return ""
}

func copyHeader(dst, src *http.Request, name string) {
	if value := strings.TrimSpace(src.Header.Get(name)); value != "" {
		dst.Header.Set(name, value)
	}
}

func (p *Proxy) baseRecord(taskID, stepID string, decision routing.Decision, model, hash string) metrics.StepRecord {
	return metrics.StepRecord{
		TaskID:             taskID,
		StepID:             stepID,
		JobClass:           decision.JobClass,
		Tier:               decision.Tier,
		ModelID:            model,
		Reason:             decision.Reason,
		SuggestionTier:     decision.SuggestionTier,
		SuggestionAccepted: decision.SuggestionAccepted,
		PrefixSHA256:       hash,
		UsageStatus:        metrics.StatusUncertain,
	}
}

func applyUsage(record *metrics.StepRecord, parsed usage.Usage, cfg config.Config, tier string) {
	if parsed.Status != usage.Measured || parsed.Input == nil || parsed.Output == nil {
		record.UsageStatus = metrics.StatusUncertain
		record.InputTokens = nil
		record.OutputTokens = nil
		record.CacheHitTokens = nil
		record.CostProxy = nil
		record.CostProxyLabel = nil
		return
	}
	record.UsageStatus = metrics.StatusMeasured
	record.InputTokens = parsed.Input
	record.OutputTokens = parsed.Output
	record.CacheHitTokens = parsed.Cache
	if rate, ok := cfg.Rate(tier); ok {
		amount := float64(*parsed.Input)/1_000_000*rate.InputPerMillion + float64(*parsed.Output)/1_000_000*rate.OutputPerMillion
		label := fmt.Sprintf("proxy: tokens × published rate (%s); not a charge", rate.Source)
		record.CostProxy = &amount
		record.CostProxyLabel = &label
	}
}

func forceModel(body []byte, model string) ([]byte, error) {
	obj, err := jsonbody.Parse(body)
	if err != nil {
		return nil, err
	}
	obj.DropPrefix("conductor_")
	if err := obj.Set("model", model); err != nil {
		return nil, err
	}
	return obj.Marshal()
}

func (p *Proxy) prefixHash(proto string) (string, error) {
	if proto != translate.ProtoMessages {
		return p.Config.Prefix.SHA256()
	}
	return prefix.HashRequest(p.Config.Prefix.System, translate.ClientTools(proto, p.Config.Prefix.Tools))
}

func hashBody(proto string, body []byte) (string, error) {
	var root map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil {
		return "", err
	}
	system := ""
	var tools []any
	switch proto {
	case translate.ProtoMessages:
		text, err := translate.SystemText(root["system"])
		if err != nil {
			return "", err
		}
		system = text
		tools, _ = root["tools"].([]any)
	case translate.ProtoResponses, translate.ProtoChat:
		var parts []string
		key := "messages"
		if proto == translate.ProtoResponses {
			key = "input"
			if instructions, ok := root["instructions"].(string); ok && instructions != "" {
				parts = append(parts, instructions)
			}
		}
		messages, _ := root[key].([]any)
		for _, item := range messages {
			msg, _ := item.(map[string]any)
			text, instruction, err := translate.InstructionText(msg)
			if err != nil {
				return "", err
			}
			if instruction && text != "" {
				parts = append(parts, text)
			}
		}
		system = strings.Join(parts, "\n")
		tools, _ = root["tools"].([]any)
	default:
		tools, _ = root["tools"].([]any)
	}

	if system == "" && len(tools) == 0 {
		return prefix.HashRequest("", []any{})
	}
	return prefix.HashRequest(system, tools)
}

func protocolOf(method, path string) (string, bool) {
	path = strings.TrimSuffix(path, "/")
	switch path {
	case "/v1/chat/completions", "/chat/completions":
		return translate.ProtoChat, method == http.MethodPost
	case "/v1/responses", "/responses":
		return translate.ProtoResponses, method == http.MethodPost
	case "/v1/messages", "/messages":
		return translate.ProtoMessages, method == http.MethodPost
	case "/v1/embeddings", "/embeddings":
		return translate.ProtoEmbeddings, method == http.MethodPost
	case "/v1/models", "/models":
		return translate.ProtoModels, method == http.MethodGet
	default:
		return "", false
	}
}

func protocolOfRequest(r *http.Request) string {
	proto, _ := protocolOf(r.Method, r.URL.Path)
	return proto
}

func boolField(obj jsonbody.Object, key string) bool {
	var value bool
	if err := json.Unmarshal(obj.Values[key], &value); err != nil {
		return false
	}
	return value
}

func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return value
}

func decodeArray(raw json.RawMessage) []any {
	if len(raw) == 0 {
		return nil
	}
	var value []any
	_ = json.Unmarshal(raw, &value)
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// NewTaskID allocates a receiver-owned task identity for a fresh request.
func NewTaskID() string { return newID("t") }

func newID(prefix string) string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return prefix + hex.EncodeToString(buf)
}

func elapsedMS(start time.Time) int {
	return int((time.Since(start) + time.Millisecond/2) / time.Millisecond)
}

type proxyAuthError struct{ msg string }

func (e proxyAuthError) Error() string { return e.msg }

func authorizeProxy(src *http.Request) error {
	want := strings.TrimSpace(os.Getenv(config.ProxyTokenEnv))
	if want == "" {
		return proxyAuthError{config.ProxyTokenEnv + " is required when a tier uses an environment key"}
	}
	return matchProxyToken(src, want, "proxy token is required to use the configured provider key")
}

func authorizeNetwork(src *http.Request) error {
	want := strings.TrimSpace(os.Getenv(config.ProxyTokenEnv))
	if want == "" {
		return proxyAuthError{config.ProxyTokenEnv + " is required for listen_mode network"}
	}
	return matchProxyToken(src, want, "proxy token is required")
}

func matchProxyToken(src *http.Request, want, mismatch string) error {
	got := strings.TrimSpace(src.Header.Get(config.ProxyTokenHeader))
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return proxyAuthError{mismatch}
	}
	return nil
}

func sseEventClosed(ready []byte) bool {
	return bytes.HasSuffix(ready, []byte("\n\n")) || bytes.HasSuffix(ready, []byte("\n\r\n"))
}

func streamDone(line []byte) bool {
	text := strings.TrimSpace(string(line))
	if !strings.HasPrefix(text, "data:") {
		return false
	}
	data := strings.TrimSpace(strings.TrimPrefix(text, "data:"))
	if data == "[DONE]" {
		return true
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(data), &obj); err != nil {
		return false
	}
	switch obj["type"] {
	case "message_stop", "response.completed", "response.incomplete", "response.failed", "error":
		return true
	default:
		return false
	}
}

func providerError(payload []byte) bool {
	var obj map[string]any
	if err := json.Unmarshal(payload, &obj); err != nil {
		return false
	}
	kind, _ := obj["type"].(string)
	if kind == "error" || kind == "response.failed" || obj["status"] == "failed" {
		return true
	}
	if _, ok := obj["error"].(map[string]any); ok {
		return true
	}
	response, _ := obj["response"].(map[string]any)
	if response == nil {
		return false
	}
	if response["status"] == "failed" {
		return true
	}
	_, ok := response["error"].(map[string]any)
	return ok
}

func (p *Proxy) applyReview(w http.ResponseWriter, r *http.Request, base *metrics.StepRecord, proto string, prepared []byte, stream bool, decision routing.Decision, tier config.Tier, hash string) ([]byte, bool, func()) {
	verdict, hopErr := p.Hop.Review(aiuditor.WithResponseBudget(r.Context(), reserveBody, releaseBody), aiuditor.Envelope{
		Request: aiuditor.Request{
			Protocol: proto,
			Method:   r.Method,
			Path:     r.URL.Path,
			Stream:   stream,
			Headers:  aiuditor.FilterHeaders(r.Header),
			Body:     prepared,
		},
		Routing: aiuditor.Routing{
			JobClass:           decision.JobClass,
			Tier:               decision.Tier,
			ModelID:            tier.Model,
			Reason:             decision.Reason,
			SuggestionTier:     decision.SuggestionTier,
			SuggestionAccepted: decision.SuggestionAccepted,
			PrefixSHA256:       hash,
		},
	})
	if hopErr != nil {
		if p.Config.AIU.FailClosed || errors.Is(hopErr, aiuditor.ErrResponseBudget) {
			msg := "aiuditor unreachable"
			if errors.Is(hopErr, aiuditor.ErrResponseBudget) {
				msg = "request body budget exceeded"
			}
			base.Error = &msg
			base.UsageStatus = metrics.StatusUncertain
			p.finish(w, r, http.StatusServiceUnavailable, nil, *base, nil, false)
			return prepared, true, verdict.Release
		}
		return prepared, false, verdict.Release
	}
	switch verdict.Action {
	case aiuditor.Deny:
		msg := "aiuditor deny: " + aiuditor.ReasonText(verdict.Reason)
		base.Error = &msg
		base.UsageStatus = metrics.StatusUncertain
		p.finish(w, r, http.StatusForbidden, nil, *base, nil, false)
		return prepared, true, verdict.Release
	case aiuditor.Modify:
		if len(verdict.Body) == 0 || !json.Valid(verdict.Body) {
			msg := "aiuditor modify was not JSON"
			base.Error = &msg
			base.UsageStatus = metrics.StatusUncertain
			p.finish(w, r, http.StatusBadGateway, nil, *base, nil, false)
			return prepared, true, verdict.Release
		}
		prepared = forceStreamFlag(verdict.Body, stream)
		if next, err := hashBody(proto, prepared); err == nil {
			base.PrefixSHA256 = next
		}
	}
	return prepared, false, verdict.Release
}

// upstreamClient reuses one bounded transport for every provider call.
// A fresh transport per request would keep its own idle sockets.
func (p *Proxy) upstreamClient(timeout time.Duration, stream bool) *http.Client {
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport:     p.sharedTransport(),
	}
	if !stream && timeout > 0 {
		client.Timeout = timeout
	}
	return client
}

func taskIDRequired(accountingURL string, tier config.Tier, proto string) bool {
	if accountingURL != "" || tier.TaskTokenLimit > 0 {
		return true
	}
	return tier.TaskOutputLimit > 0 && proto != translate.ProtoEmbeddings
}

func (p *Proxy) sharedTransport() *http.Transport {
	p.transportOnce.Do(func() {
		var headerTimeout time.Duration
		for _, candidate := range []config.Tier{p.Config.Cheap, p.Config.Frontier} {
			if candidate.Configured() && candidate.Timeout > headerTimeout {
				headerTimeout = candidate.Timeout
			}
		}
		if headerTimeout <= 0 {
			headerTimeout = 60 * time.Second
		}
		p.transport = &http.Transport{
			Proxy:                 nil,
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
			ResponseHeaderTimeout: headerTimeout,
			MaxIdleConns:          10,
			MaxIdleConnsPerHost:   2,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
		}
	})
	return p.transport
}

func forceStreamFlag(body []byte, stream bool) []byte {
	obj, err := jsonbody.Parse(body)
	if err != nil {
		return body
	}
	if stream {
		if err := obj.Set("stream", true); err != nil {
			return body
		}
	} else {
		obj.Delete("stream")
	}
	raw, err := obj.Marshal()
	if err != nil {
		return body
	}
	return raw
}

func ssePayload(line []byte) []byte {
	text := strings.TrimSpace(string(line))
	if !strings.HasPrefix(text, "data:") {
		return nil
	}
	data := strings.TrimSpace(strings.TrimPrefix(text, "data:"))
	if data == "" || data == "[DONE]" {
		return nil
	}
	return []byte(data)
}

func redact(body []byte, secrets []string) []byte {
	if len(body) == 0 {
		return body
	}
	out := body
	for _, secret := range secrets {
		if len(secret) < 8 {
			continue
		}
		out = bytes.ReplaceAll(out, []byte(secret), []byte("[redacted]"))
	}
	return out
}

func errorText(record metrics.StepRecord) string {
	if record.Error != nil && *record.Error != "" {
		return *record.Error
	}
	return "request failed"
}

func stampRun(w http.ResponseWriter, record metrics.StepRecord) {
	if record.TaskID != "" {
		w.Header().Set("X-Conductor-Task", record.TaskID)
	}
	if record.StepID != "" {
		w.Header().Set("X-Conductor-Step", record.StepID)
	}
}

func acceptsNotes(proto string) bool {
	return proto == translate.ProtoChat || proto == translate.ProtoMessages
}

func (p *Proxy) insertNotes(body []byte, query, taskID, stepID string, trustIDs bool) ([]byte, notes.Result, error) {
	result, err := p.Notes.Retrieve(query, taskID, stepID, trustIDs)
	if err != nil || result.Block == "" {
		return body, result, err
	}
	updated, err := notes.InsertAfterPrefix(body, result.Block)
	if err != nil {
		return body, result, err
	}
	return updated, result, nil
}

func userQuery(body []byte) string {
	var root struct {
		Messages []struct {
			Role    string
			Content json.RawMessage
		}
		Input json.RawMessage
	}
	if err := json.Unmarshal(body, &root); err != nil {
		return ""
	}
	seenUser := false
	var last string
	for _, msg := range root.Messages {
		if msg.Role != "user" {
			continue
		}
		var content any
		if json.Unmarshal(msg.Content, &content) != nil {
			continue
		}
		text, ok := contentText(content)
		if !ok {
			continue
		}
		seenUser = true
		last = text
	}
	if seenUser {
		return last
	}
	var input string
	if json.Unmarshal(root.Input, &input) == nil {
		return input
	}
	return ""
}

func contentText(content any) (string, bool) {
	switch value := content.(type) {
	case string:
		return value, true
	case []any:
		toolResultsOnly := len(value) > 0
		for _, part := range value {
			item, ok := part.(map[string]any)
			if !ok || item["type"] != "tool_result" {
				toolResultsOnly = false
				break
			}
		}
		if toolResultsOnly {
			return "", false
		}
		var b strings.Builder
		for _, part := range value {
			switch item := part.(type) {
			case string:
				b.WriteString(item)
			case map[string]any:
				kind, _ := item["type"].(string)
				text, isText := item["text"].(string)
				if kind == "text" || kind == "input_text" || kind == "output_text" || (kind == "" && isText) {
					b.WriteString(text)
				}
			}
		}
		return b.String(), true
	default:
		return "", false
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(payload)
}

func mustJSON(payload any) []byte {
	raw, _ := json.Marshal(payload)
	return raw
}

// readReserved buffers a provider response and charges every byte to the
// shared body budget. The caller releases held when it drops the buffer.
// A failed reservation returns the bytes already kept so they can be released.
func readReserved(r io.Reader, limit int) ([]byte, int, error) {
	var body []byte
	held := 0
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if len(body)+n > limit {
				return body, held, errResponseTooLarge
			}
			if !reserveBody(n) {
				return body, held, errBodyBudget
			}
			held += n
			body = append(body, buf[:n]...)
		}
		if err == io.EOF {
			return body, held, nil
		}
		if err != nil {
			return body, held, err
		}
	}
}

func readRequestBody(r io.Reader) ([]byte, error) {
	var body []byte
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if len(body)+n > maxBody {
				return body, errBodyTooLarge
			}
			if !reserveBody(n) {
				return body, errBodyBudget
			}
			body = append(body, buf[:n]...)
		}
		if err == io.EOF {
			return body, nil
		}
		if err != nil {
			return body, err
		}
	}
}

func reserveBody(n int) bool {
	bodyMu.Lock()
	defer bodyMu.Unlock()
	if n < 0 || bodyUsed > bodyLimit || n > bodyLimit-bodyUsed {
		return false
	}
	bodyUsed += n
	return true
}

func releaseBody(n int) {
	if n == 0 {
		return
	}
	bodyMu.Lock()
	bodyUsed -= n
	if bodyUsed < 0 {
		bodyUsed = 0
	}
	bodyMu.Unlock()
}

func replaceReservation(oldN, newN int) bool {
	bodyMu.Lock()
	defer bodyMu.Unlock()
	used := bodyUsed - oldN
	if used < 0 {
		used = 0
	}
	if newN < 0 || used > bodyLimit || newN > bodyLimit-used {
		bodyUsed = used
		return false
	}
	bodyUsed = used + newN
	return true
}

// deadlineWriter bounds each response write. A client that stops reading
// cannot hold a connection slot for the rest of a long stream.
type deadlineWriter struct {
	http.ResponseWriter
	idle time.Duration
}

func (d deadlineWriter) Write(p []byte) (int, error) {
	d.arm()
	return d.ResponseWriter.Write(p)
}

func (d deadlineWriter) Flush() {
	_ = d.FlushError()
}

func (d deadlineWriter) FlushError() error {
	d.arm()
	return http.NewResponseController(d.ResponseWriter).Flush()
}

func (d deadlineWriter) Unwrap() http.ResponseWriter { return d.ResponseWriter }

func flushResult(w http.ResponseWriter) error {
	return http.NewResponseController(w).Flush()
}

func (d deadlineWriter) arm() {
	if d.idle <= 0 {
		return
	}
	_ = http.NewResponseController(d.ResponseWriter).SetWriteDeadline(time.Now().Add(d.idle))
}

func flush(w http.ResponseWriter) {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}
