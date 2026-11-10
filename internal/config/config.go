// Package config loads endpoint and proxy settings.
//
// API keys are never stored in the file. api_key_env names the environment
// variable that holds a key.
package config

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hyperme-sh/hypermesh-conductor/internal/prefix"
	"github.com/hyperme-sh/hypermesh-conductor/internal/routing"
)

const (
	ProviderOpenAI           = "openai"
	ProviderAnthropic        = "anthropic"
	ProviderOpenAICompatible = "openai-compatible"

	AuthPassthrough      = "passthrough"
	AuthEnv              = "env"
	AuthPassthroughOrEnv = "passthrough_or_env"

	AIUModeStub = "stub"
	AIUModeHTTP = "http"

	DefaultListen    = "127.0.0.1:8787"
	ProxyTokenEnv    = "CONDUCTOR_PROXY_TOKEN"
	ProxyTokenHeader = "X-Conductor-Token"

	ListenModeLoopback = "loopback"
	ListenModeNetwork  = "network"

	// FallbackClass is the built-in job class used when a request names no
	// class and the config has no default_class. It selects the cheap tier,
	// the first tier in the endpoints file.
	FallbackClass = "parse"
	// FrontierFallbackClass selects the frontier tier when the cheap tier is
	// not configured.
	FrontierFallbackClass = "code"
)

var forbiddenKeys = map[string]struct{}{
	"api_key": {}, "apikey": {}, "api_token": {}, "access_token": {},
	"authorization": {}, "bearer": {}, "password": {}, "secret": {}, "token": {},
}

// Tier is one configured destination.
type Tier struct {
	InputCounter    string
	ContextWindow   int
	TaskTokenLimit  int
	TaskOutputLimit int
	RecipeDigest    string
	OutputLimit     int
	Name            string
	Provider        string
	BaseURL         string
	Model           string
	APIKeyEnv       string
	Timeout         time.Duration
	Auth            string
	IncludeUsage    bool
	// Gateway marks an OpenAI-compatible Hypermesh gateway tier. The model
	// written upstream is a catalog id that tier recognizes.
	Gateway bool
}

// TLSFiles are the certificate and key paths for listen_mode network.
// The key stays on disk. It is not stored in the endpoints file.
type TLSFiles struct {
	CertFile string
	KeyFile  string
}

// UsesEnvKey reports whether this tier can attach an environment key.
func (t Tier) UsesEnvKey() bool {
	if t.APIKeyEnv == "" {
		return false
	}
	return t.Auth == AuthEnv || t.Auth == AuthPassthroughOrEnv
}

// RequireProxyToken fails when an environment key can be attached and the
// caller token is not configured. The token is not read from the config file.
func (c Config) RequireProxyToken() error {
	if !c.Cheap.UsesEnvKey() && !c.Frontier.UsesEnvKey() {
		return nil
	}
	if strings.TrimSpace(os.Getenv(ProxyTokenEnv)) == "" {
		return fmt.Errorf("%s is required when a tier uses an environment key", ProxyTokenEnv)
	}
	return nil
}

// RequireListen fails closed for the opt-in non-loopback mode. That mode
// needs a TLS certificate, a TLS key, and CONDUCTOR_PROXY_TOKEN before the
// process binds.
func (c Config) RequireListen() error {
	if c.ListenMode != ListenModeNetwork {
		return nil
	}
	if strings.TrimSpace(c.TLS.CertFile) == "" || strings.TrimSpace(c.TLS.KeyFile) == "" {
		return fmt.Errorf("listen_mode network requires tls.cert_file and tls.key_file")
	}
	if _, err := os.Stat(c.TLS.CertFile); err != nil {
		return fmt.Errorf("tls.cert_file: %w", err)
	}
	if _, err := os.Stat(c.TLS.KeyFile); err != nil {
		return fmt.Errorf("tls.key_file: %w", err)
	}
	if strings.TrimSpace(os.Getenv(ProxyTokenEnv)) == "" {
		return fmt.Errorf("%s is required for listen_mode network", ProxyTokenEnv)
	}
	return nil
}

// FallbackClass chooses a built-in class when the request and the config
// both omit one. The cheap tier is first. HTTP 400 remains only when no
// tier is configured.
func (c Config) FallbackClass() (string, error) {
	if tierConfigured(c.Cheap) {
		return FallbackClass, nil
	}
	if tierConfigured(c.Frontier) {
		return FrontierFallbackClass, nil
	}
	return "", fmt.Errorf("no tiers are configured")
}

func tierConfigured(tier Tier) bool {
	return strings.TrimSpace(tier.BaseURL) != "" || strings.TrimSpace(tier.Model) != ""
}

// Key returns the environment key when this tier is configured to read one.
// An empty APIKeyEnv means the tier has no Conductor-held key.
func (t Tier) Key() (string, error) {
	if t.APIKeyEnv == "" {
		return "", nil
	}
	value := strings.TrimSpace(os.Getenv(t.APIKeyEnv))
	if value == "" {
		return "", fmt.Errorf("%s is not set", t.APIKeyEnv)
	}
	return value, nil
}

// CostRate is a published token rate used only as a labeled proxy.
type CostRate struct {
	InputPerMillion  float64
	OutputPerMillion float64
	Source           string
}

// AIU is the AIuditor hop. The stub is in-process. HTTP posts the contract.
type AIU struct {
	Mode       string
	URL        string
	RulesFile  string
	FailClosed bool
	Timeout    time.Duration
}

// Config is the process configuration.
type Config struct {
	Listen       string
	ListenMode   string
	TLS          TLSFiles
	Journal      string
	DefaultClass string
	Cheap        Tier
	Frontier     Tier
	Prefix       prefix.Prefix
	ClassTiers   map[string]string
	CostRates    map[string]CostRate
	AIU          AIU
	Compaction   Compaction
	RulesFile    string
	Notes        NotesConfig
}

// NotesConfig is the on-disk linked-note index. An empty Dir leaves it off.
type NotesConfig struct {
	Dir         string
	DB          string
	Hops        int
	TokenBudget int
	Embed       bool
	Watch       bool
	Primer      bool
	WatchEvery  time.Duration
}

// Compaction controls tail summarization. A zero max leaves the tail unchanged.
type Compaction struct {
	MaxTailTokens  int
	RecentMessages int
}

// Endpoint returns the tier selected by routing.
func (c Config) Endpoint(tier string) (Tier, error) {
	name, err := routing.CanonicalTier(tier)
	if err != nil {
		return Tier{}, err
	}
	if name == routing.Cheap {
		return c.Cheap, nil
	}
	return c.Frontier, nil
}

// Rate returns the published proxy rate for a tier, if one was configured.
func (c Config) Rate(tier string) (CostRate, bool) {
	name, err := routing.CanonicalTier(tier)
	if err != nil {
		return CostRate{}, false
	}
	rate, ok := c.CostRates[name]
	return rate, ok
}

// Load reads a JSON config file.
func Load(path string) (Config, error) {
	rawBytes, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, fmt.Errorf("config file not found: %s", path)
		}
		return Config{}, err
	}
	var raw map[string]any
	dec := json.NewDecoder(bytesReader(rawBytes))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return Config{}, fmt.Errorf("config file is not JSON: %s", path)
	}
	if err := rejectSecrets(raw, "config"); err != nil {
		return Config{}, err
	}
	tiers, _ := raw["tiers"].(map[string]any)
	if tiers == nil {
		return Config{}, fmt.Errorf("config tiers must be an object with cheap and frontier")
	}
	for name, block := range tiers {
		if obj, ok := block.(map[string]any); ok {
			if err := rejectSecrets(obj, "tiers."+name); err != nil {
				return Config{}, err
			}
		}
	}
	cheap, err := parseTier(routing.Cheap, tiers[routing.Cheap])
	if err != nil {
		return Config{}, err
	}
	frontier, err := parseTier(routing.Frontier, tiers[routing.Frontier])
	if err != nil {
		return Config{}, err
	}
	if recipe, present := raw["recipe_file"]; present {
		name, ok := recipe.(string)
		if !ok || strings.TrimSpace(name) == "" {
			return Config{}, fmt.Errorf("recipe_file must name a recipe")
		}
		if !filepath.IsAbs(name) {
			name = filepath.Join(filepath.Dir(path), name)
		}
		if err := loadRecipe(name, &cheap, &frontier); err != nil {
			return Config{}, err
		}
	}
	prefixBlock, _ := raw["prefix"].(map[string]any)
	if prefixBlock == nil {
		return Config{}, fmt.Errorf("config prefix must be an object with system and tools")
	}
	system, _ := prefixBlock["system"].(string)
	var tools []any
	if prefixBlock["tools"] != nil {
		list, ok := prefixBlock["tools"].([]any)
		if !ok {
			return Config{}, fmt.Errorf("prefix tools must be a list")
		}
		tools = list
	}
	built, err := prefix.Build(system, tools)
	if err != nil {
		return Config{}, err
	}
	classTiers, err := parseClassTiers(raw["class_tiers"])
	if err != nil {
		return Config{}, err
	}
	rates, err := parseRates(raw["cost_proxy_rates"])
	if err != nil {
		return Config{}, err
	}
	listen, _ := raw["listen"].(string)
	if listen == "" {
		listen = DefaultListen
	}
	listenMode, tlsFiles, err := parseListen(raw, listen)
	if err != nil {
		return Config{}, err
	}
	journal, _ := raw["journal"].(string)
	defaultClass := ""
	if value, ok := raw["default_class"].(string); ok && strings.TrimSpace(value) != "" {
		defaultClass, err = routing.CanonicalClass(value)
		if err != nil {
			return Config{}, err
		}
	}
	aiu, err := parseAIU(raw["aiuditor"])
	if err != nil {
		return Config{}, err
	}
	compaction, err := parseCompaction(raw["compaction"])
	if err != nil {
		return Config{}, err
	}
	rulesFile, _ := raw["rules_file"].(string)
	notesCfg, err := parseNotes(raw["notes"])
	if err != nil {
		return Config{}, err
	}
	return Config{
		Listen:       listen,
		ListenMode:   listenMode,
		TLS:          tlsFiles,
		Journal:      journal,
		DefaultClass: defaultClass,
		Cheap:        cheap,
		Frontier:     frontier,
		Prefix:       built,
		ClassTiers:   classTiers,
		CostRates:    rates,
		AIU:          aiu,
		Compaction:   compaction,
		RulesFile:    rulesFile,
		Notes:        notesCfg,
	}, nil
}

func parseNotes(value any) (NotesConfig, error) {
	out := NotesConfig{Hops: 2, TokenBudget: 800, Watch: true, Primer: true, WatchEvery: time.Second}
	if value == nil {
		return NotesConfig{}, nil
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return NotesConfig{}, fmt.Errorf("notes must be an object")
	}
	out.Dir, _ = obj["dir"].(string)
	out.DB, _ = obj["db"].(string)
	if raw, ok := obj["hops"]; ok && raw != nil {
		number, ok := asFloat(raw)
		if !ok || number < 0 {
			return NotesConfig{}, fmt.Errorf("notes.hops must be a non-negative number")
		}
		out.Hops = int(number)
	}
	if raw, ok := obj["token_budget"]; ok && raw != nil {
		number, ok := asFloat(raw)
		if !ok || number < 0 {
			return NotesConfig{}, fmt.Errorf("notes.token_budget must be a non-negative number")
		}
		out.TokenBudget = int(number)
	}
	if flag, ok := obj["embed"].(bool); ok {
		out.Embed = flag
	}
	if flag, ok := obj["watch"].(bool); ok {
		out.Watch = flag
	}
	if flag, ok := obj["primer"].(bool); ok {
		out.Primer = flag
	}
	if out.Dir == "" {
		return NotesConfig{}, nil
	}
	return out, nil
}

func parseCompaction(value any) (Compaction, error) {
	out := Compaction{RecentMessages: 2}
	if value == nil {
		return Compaction{}, nil
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return Compaction{}, fmt.Errorf("compaction must be an object")
	}
	if raw, ok := obj["max_tail_tokens"]; ok && raw != nil {
		number, ok := asFloat(raw)
		if !ok || number < 0 {
			return Compaction{}, fmt.Errorf("compaction.max_tail_tokens must be a non-negative number")
		}
		out.MaxTailTokens = int(number)
	}
	if raw, ok := obj["recent_messages"]; ok && raw != nil {
		number, ok := asFloat(raw)
		if !ok || number < 0 {
			return Compaction{}, fmt.Errorf("compaction.recent_messages must be a non-negative number")
		}
		out.RecentMessages = int(number)
	}
	return out, nil
}

func parseListen(raw map[string]any, listen string) (string, TLSFiles, error) {
	mode := ListenModeLoopback
	if value, ok := raw["listen_mode"].(string); ok && strings.TrimSpace(value) != "" {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case ListenModeLoopback:
			mode = ListenModeLoopback
		case ListenModeNetwork:
			mode = ListenModeNetwork
		default:
			return "", TLSFiles{}, fmt.Errorf("listen_mode must be loopback or network")
		}
	}
	var tlsFiles TLSFiles
	if raw["tls"] != nil {
		block, ok := raw["tls"].(map[string]any)
		if !ok {
			return "", TLSFiles{}, fmt.Errorf("tls must be an object with cert_file and key_file")
		}
		if err := rejectSecrets(block, "tls"); err != nil {
			return "", TLSFiles{}, err
		}
		cert, _ := block["cert_file"].(string)
		key, _ := block["key_file"].(string)
		tlsFiles = TLSFiles{CertFile: strings.TrimSpace(cert), KeyFile: strings.TrimSpace(key)}
	}
	if mode == ListenModeLoopback {
		if err := ValidateListen(listen); err != nil {
			return "", TLSFiles{}, err
		}
		if tlsFiles.CertFile != "" || tlsFiles.KeyFile != "" {
			return "", TLSFiles{}, fmt.Errorf("tls.cert_file and tls.key_file require listen_mode network")
		}
		return mode, TLSFiles{}, nil
	}
	if _, _, err := net.SplitHostPort(listen); err != nil {
		return "", TLSFiles{}, fmt.Errorf("listen must be host:port: %s", listen)
	}
	if tlsFiles.CertFile == "" || tlsFiles.KeyFile == "" {
		return "", TLSFiles{}, fmt.Errorf("listen_mode network requires tls.cert_file and tls.key_file")
	}
	if _, err := os.Stat(tlsFiles.CertFile); err != nil {
		return "", TLSFiles{}, fmt.Errorf("tls.cert_file: %w", err)
	}
	if _, err := os.Stat(tlsFiles.KeyFile); err != nil {
		return "", TLSFiles{}, fmt.Errorf("tls.key_file: %w", err)
	}
	return mode, tlsFiles, nil
}

// ValidateListen allows loopback addresses only.
func ValidateListen(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("listen must be host:port: %s", addr)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("listen must be a loopback address, got %s", addr)
}

func parseTier(name string, block any) (Tier, error) {
	obj, ok := block.(map[string]any)
	if !ok {
		return Tier{}, fmt.Errorf("tiers.%s must be an object", name)
	}
	baseURL, _ := obj["base_url"].(string)
	model, _ := obj["model"].(string)
	if strings.TrimSpace(baseURL) == "" {
		return Tier{}, fmt.Errorf("tiers.%s.base_url must be a string", name)
	}
	if strings.TrimSpace(model) == "" {
		return Tier{}, fmt.Errorf("tiers.%s.model must be a string", name)
	}
	if err := validateBaseURL(name, baseURL); err != nil {
		return Tier{}, err
	}
	provider := ProviderOpenAICompatible
	if value, ok := obj["provider"].(string); ok && value != "" {
		switch value {
		case ProviderOpenAI, ProviderAnthropic, ProviderOpenAICompatible:
			provider = value
		default:
			return Tier{}, fmt.Errorf("tiers.%s.provider must be openai, anthropic, or openai-compatible", name)
		}
	}
	counter := ""
	if v, present := obj["input_counter"]; present {
		base, err := url.Parse(baseURL)
		if v != "vllm" || provider != ProviderOpenAICompatible || err != nil || base.RawQuery != "" || base.Fragment != "" || base.RawPath != "" || !strings.HasSuffix(strings.TrimRight(base.Path, "/"), "/v1") {
			return Tier{}, fmt.Errorf("tiers.%s.input_counter requires vllm on an openai-compatible /v1 endpoint", name)
		}
		counter = "vllm"
	}
	auth := AuthPassthroughOrEnv
	if value, ok := obj["auth"].(string); ok && value != "" {
		switch value {
		case AuthPassthrough, AuthEnv, AuthPassthroughOrEnv:
			auth = value
		default:
			return Tier{}, fmt.Errorf("tiers.%s.auth must be passthrough, env, or passthrough_or_env", name)
		}
	}
	envName := ""
	if value, ok := obj["api_key_env"]; ok && value != nil {
		text, ok := value.(string)
		if !ok || !envNameOK(text) {
			return Tier{}, fmt.Errorf("tiers.%s.api_key_env must name an environment variable", name)
		}
		envName = text
	}
	timeout := 60.0
	if value, ok := obj["timeout_seconds"]; ok && value != nil {
		number, ok := asFloat(value)
		if !ok || number <= 0 {
			return Tier{}, fmt.Errorf("tiers.%s.timeout_seconds must be a positive number", name)
		}
		timeout = number
	}
	includeUsage := false
	if value, ok := obj["include_stream_usage"].(bool); ok {
		includeUsage = value
	}
	gateway := false
	if value, ok := obj["gateway"].(bool); ok {
		gateway = value
	} else if _, present := obj["gateway"]; present {
		return Tier{}, fmt.Errorf("tiers.%s.gateway must be a boolean", name)
	}
	catalogModel := ""
	if value, ok := obj["catalog_model"].(string); ok {
		catalogModel = value
	} else if _, present := obj["catalog_model"]; present && obj["catalog_model"] != nil {
		return Tier{}, fmt.Errorf("tiers.%s.catalog_model must be a string", name)
	}
	if gateway {
		if provider == ProviderAnthropic {
			return Tier{}, fmt.Errorf("tiers.%s.gateway requires provider openai or openai-compatible", name)
		}
		resolved, err := ResolveCatalogModel(model, catalogModel)
		if err != nil {
			return Tier{}, fmt.Errorf("tiers.%s: %w", name, err)
		}
		model = resolved
	} else if strings.TrimSpace(catalogModel) != "" {
		return Tier{}, fmt.Errorf("tiers.%s.catalog_model requires gateway true", name)
	}
	return Tier{
		Name:         name,
		InputCounter: counter,
		Provider:     provider,
		BaseURL:      strings.TrimRight(baseURL, "/"),
		Model:        model,
		APIKeyEnv:    envName,
		Timeout:      time.Duration(timeout * float64(time.Second)),
		Auth:         auth,
		IncludeUsage: includeUsage,
		Gateway:      gateway,
	}, nil
}

func validateBaseURL(tier, baseURL string) error {
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		return fmt.Errorf("tiers.%s.base_url must be an http or https URL", tier)
	}
	rest := baseURL
	switch {
	case strings.HasPrefix(rest, "https://"):
		rest = strings.TrimPrefix(rest, "https://")
	case strings.HasPrefix(rest, "http://"):
		rest = strings.TrimPrefix(rest, "http://")
	}
	if rest == "" || strings.HasPrefix(rest, "/") {
		return fmt.Errorf("tiers.%s.base_url must be an http or https URL", tier)
	}
	host := rest
	if i := strings.Index(host, "/"); i >= 0 {
		host = host[:i]
	}
	if strings.Contains(host, "@") {
		return fmt.Errorf("tiers.%s.base_url must not carry credentials", tier)
	}
	path := ""
	if i := strings.Index(rest, "/"); i >= 0 {
		path = strings.TrimRight(rest[i:], "/")
	}
	if strings.HasSuffix(path, "/chat/completions") || strings.HasSuffix(path, "/embeddings") || strings.HasSuffix(path, "/messages") || strings.HasSuffix(path, "/responses") {
		return fmt.Errorf("tiers.%s.base_url is the API root, such as https://host/v1", tier)
	}
	if strings.HasPrefix(baseURL, "http://") && !loopbackURL(baseURL) {
		return fmt.Errorf("tiers.%s.base_url must use https off the loopback interface", tier)
	}
	return nil
}

func parseClassTiers(value any) (map[string]string, error) {
	if value == nil {
		return map[string]string{}, nil
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("class_tiers must be an object")
	}
	out := make(map[string]string, len(obj))
	for name, tier := range obj {
		if _, known := routing.DefaultTiers[name]; !known {
			return nil, fmt.Errorf("class_tiers has unknown job class '%s'", name)
		}
		text, ok := tier.(string)
		if !ok {
			return nil, fmt.Errorf("class_tiers.%s must be cheap or frontier", name)
		}
		canonical, err := routing.CanonicalTier(text)
		if err != nil {
			return nil, err
		}
		out[name] = canonical
	}
	return out, nil
}

func parseRates(value any) (map[string]CostRate, error) {
	if value == nil {
		return map[string]CostRate{}, nil
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("cost_proxy_rates must be an object")
	}
	out := make(map[string]CostRate, len(obj))
	for tier, spec := range obj {
		canonical, err := routing.CanonicalTier(tier)
		if err != nil {
			return nil, err
		}
		block, ok := spec.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("cost_proxy_rates.%s must be an object", tier)
		}
		source, _ := block["source"].(string)
		if strings.TrimSpace(source) == "" {
			return nil, fmt.Errorf("cost_proxy_rates.%s.source must name the published rate", tier)
		}
		inRate, ok := asFloat(block["input_per_million"])
		if !ok || inRate < 0 {
			return nil, fmt.Errorf("cost_proxy_rates.%s.input_per_million must be a non-negative number", tier)
		}
		outRate, ok := asFloat(block["output_per_million"])
		if !ok || outRate < 0 {
			return nil, fmt.Errorf("cost_proxy_rates.%s.output_per_million must be a non-negative number", tier)
		}
		out[canonical] = CostRate{InputPerMillion: inRate, OutputPerMillion: outRate, Source: strings.TrimSpace(source)}
	}
	return out, nil
}

func parseAIU(value any) (AIU, error) {
	aiu := AIU{Mode: AIUModeStub, FailClosed: true, Timeout: 5 * time.Second}
	if value == nil {
		return aiu, nil
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return AIU{}, fmt.Errorf("aiuditor must be an object")
	}
	if err := rejectSecrets(obj, "aiuditor"); err != nil {
		return AIU{}, err
	}
	if mode, ok := obj["mode"].(string); ok && mode != "" {
		switch mode {
		case AIUModeStub, AIUModeHTTP:
			aiu.Mode = mode
		default:
			return AIU{}, fmt.Errorf("aiuditor.mode must be stub or http")
		}
	}
	if url, ok := obj["url"].(string); ok {
		aiu.URL = strings.TrimSpace(url)
	}
	if file, ok := obj["rules_file"].(string); ok {
		aiu.RulesFile = file
	}
	if flag, ok := obj["fail_closed"].(bool); ok {
		aiu.FailClosed = flag
	}
	if value, ok := obj["timeout_seconds"]; ok && value != nil {
		number, ok := asFloat(value)
		if !ok || number <= 0 {
			return AIU{}, fmt.Errorf("aiuditor.timeout_seconds must be a positive number")
		}
		aiu.Timeout = time.Duration(number * float64(time.Second))
	}
	if aiu.Mode == AIUModeHTTP {
		if err := validateHTTPURL(aiu.URL); err != nil {
			return AIU{}, fmt.Errorf("aiuditor.url: %w", err)
		}
	}
	return aiu, nil
}

func validateHTTPURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("url is required when mode is http")
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		return fmt.Errorf("must be an http or https URL")
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(raw, "https://"), "http://")
	if rest == "" || strings.HasPrefix(rest, "/") {
		return fmt.Errorf("must be an http or https URL")
	}
	host := rest
	if i := strings.Index(host, "/"); i >= 0 {
		host = host[:i]
	}
	if host == "" || strings.Contains(host, "@") {
		return fmt.Errorf("must not carry credentials")
	}
	if strings.HasPrefix(raw, "http://") && !loopbackURL(raw) {
		return fmt.Errorf("must use https off the loopback interface")
	}
	return nil
}

func loopbackURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := parsed.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func rejectSecrets(obj map[string]any, where string) error {
	for key := range obj {
		if _, bad := forbiddenKeys[strings.ToLower(key)]; bad {
			return fmt.Errorf("%s must not contain %s; name an environment variable in api_key_env", where, key)
		}
	}
	return nil
}

func envNameOK(value string) bool {
	if value == "" || value[0] >= '0' && value[0] <= '9' {
		return false
	}
	for _, char := range value {
		if (char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '_' {
			continue
		}
		return false
	}
	return true
}

func asFloat(value any) (float64, bool) {
	switch n := value.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

func bytesReader(b []byte) *strings.Reader { return strings.NewReader(string(b)) }
