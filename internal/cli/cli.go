// Package cli is the conductor command: route, run, summary, and serve.
package cli

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"

	"github.com/hyperme-sh/hypermesh-conductor/internal/aiuditor"
	"github.com/hyperme-sh/hypermesh-conductor/internal/config"
	"github.com/hyperme-sh/hypermesh-conductor/internal/history"
	"github.com/hyperme-sh/hypermesh-conductor/internal/metrics"
	"github.com/hyperme-sh/hypermesh-conductor/internal/proxy"
	"github.com/hyperme-sh/hypermesh-conductor/internal/routing"
	"github.com/hyperme-sh/hypermesh-conductor/internal/rules"
)

// Main runs one invocation. args do not include the program name.
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: conductor <route|run|summary|serve>")
		return 2
	}
	switch args[0] {
	case "route":
		return routeCmd(args[1:], stdout, stderr)
	case "summary":
		return summaryCmd(args[1:], stdout, stderr)
	case "serve":
		return serveCmd(args[1:], stdout, stderr)
	case "run":
		return runCmd(args[1:], stdout, stderr)
	case "dream":
		return dreamCmd(args[1:], stdout, stderr)
	case "rules":
		return rulesCmd(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprintln(stdout, "usage: conductor <route|run|summary|serve|dream|rules>")
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %s\n", args[0])
		return 2
	}
}

func routeCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("route", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "endpoints file")
	className := fs.String("class", "", "job class")
	suggest := fs.String("suggest", "", "suggested tier")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, code := load(configPath, stderr)
	if code != 0 {
		return code
	}
	if strings.TrimSpace(*className) == "" {
		fmt.Fprintln(stderr, "class is required")
		return 2
	}
	policy, err := rules.Load(cfg.RulesFile)
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return 2
	}
	decision, err := routing.Route(*className, *suggest, rules.EffectiveClassTiers(cfg.ClassTiers, policy, *className, 0))
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return 2
	}
	tier, err := cfg.Endpoint(decision.Tier)
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return 2
	}
	hash, err := cfg.Prefix.SHA256()
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return 2
	}
	payload := map[string]any{
		"job_class":           decision.JobClass,
		"tier":                decision.Tier,
		"model_id":            tier.Model,
		"reason":              decision.Reason,
		"suggestion_tier":     decision.SuggestionTier,
		"suggestion_accepted": decision.SuggestionAccepted,
		"prefix_sha256":       hash,
	}
	return writePretty(stdout, payload)
}

func summaryCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("summary", flag.ContinueOnError)
	fs.SetOutput(stderr)
	journal := fs.String("journal", "", "measurement journal")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *journal == "" {
		fmt.Fprintln(stderr, "journal is required")
		return 2
	}
	records, err := metrics.ReadJournal(*journal)
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return 2
	}
	return writePretty(stdout, metrics.Summarize(records))
}

func serveCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "endpoints file")
	journalPath := fs.String("journal", "", "measurement journal")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, code := load(configPath, stderr)
	if code != 0 {
		return code
	}
	if *journalPath != "" {
		cfg.Journal = *journalPath
	}
	if cfg.Journal == "" {
		fmt.Fprintln(stderr, "journal is required")
		return 2
	}
	journal, err := metrics.OpenJournal(cfg.Journal)
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return 2
	}
	if err := cfg.RequireProxyToken(); err != nil {
		fmt.Fprintln(stderr, err.Error())
		return 2
	}
	hop, err := hopFrom(cfg)
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return 2
	}
	server := proxy.New(cfg, hop, journal)
	server.Logf = func(format string, args ...any) {
		fmt.Fprintf(stderr, format+"\n", args...)
	}
	fmt.Fprintf(stdout, "listening on http://%s\n", cfg.Listen)
	if err := server.ListenAndServe(); err != nil {
		fmt.Fprintln(stderr, err.Error())
		return 1
	}
	return 0
}

func runCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "endpoints file")
	journalPath := fs.String("journal", "", "measurement journal")
	taskID := fs.String("task", "", "task id")
	stepID := fs.String("step", "", "step id")
	className := fs.String("class", "", "job class")
	suggest := fs.String("suggest", "", "suggested tier")
	message := fs.String("message", "", "user message")
	input := fs.String("input", "", "embedding input")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, code := load(configPath, stderr)
	if code != 0 {
		return code
	}
	if *journalPath == "" {
		fmt.Fprintln(stderr, "journal is required")
		return 2
	}
	journal, err := metrics.OpenJournal(*journalPath)
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return 2
	}
	if err := cfg.RequireProxyToken(); err != nil {
		fmt.Fprintln(stderr, err.Error())
		return 2
	}
	hop, err := hopFrom(cfg)
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return 2
	}
	body := map[string]any{}
	path := "/v1/chat/completions"
	if *className == "embed" || *className == "embeddings" {
		if *input == "" {
			fmt.Fprintln(stderr, "embed steps need input text")
			return 2
		}
		body["input"] = *input
		path = "/v1/embeddings"
	} else {
		if *message == "" {
			fmt.Fprintln(stderr, "a chat step needs a message")
			return 2
		}
		body["messages"] = []any{map[string]any{"role": "user", "content": *message}}
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("X-Conductor-Class", *className)
	req.Header.Set("X-Conductor-Task", *taskID)
	req.Header.Set("X-Conductor-Step", *stepID)
	if *suggest != "" {
		req.Header.Set("X-Conductor-Suggest", *suggest)
	}
	if token := strings.TrimSpace(os.Getenv(config.ProxyTokenEnv)); token != "" {
		req.Header.Set(config.ProxyTokenHeader, token)
	}
	rec := httptest.NewRecorder()
	proxy.New(cfg, hop, journal).ServeHTTP(rec, req)
	if rec.Code == http.StatusBadRequest {
		fmt.Fprintln(stderr, strings.TrimSpace(rec.Body.String()))
		return 2
	}
	records, err := metrics.ReadJournal(*journalPath)
	if err != nil || len(records) == 0 {
		fmt.Fprintln(stderr, "journal was not written")
		return 1
	}
	shown := records[len(records)-1].Wire()
	payload := map[string]any{}
	encoded, _ := json.Marshal(shown)
	_ = json.Unmarshal(encoded, &payload)
	payload["content"] = contentOf(rec.Body.Bytes())
	if err := writePretty(stdout, payload); err != 0 {
		return err
	}
	if rec.Code >= 400 {
		return 1
	}
	return 0
}

func contentOf(body []byte) any {
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return nil
	}
	if choices, ok := root["choices"].([]any); ok && len(choices) > 0 {
		choice, _ := choices[0].(map[string]any)
		message, _ := choice["message"].(map[string]any)
		if text, ok := message["content"].(string); ok {
			return text
		}
	}
	if content, ok := root["content"].([]any); ok {
		var parts []string
		for _, block := range content {
			obj, _ := block.(map[string]any)
			if text, ok := obj["text"].(string); ok {
				parts = append(parts, text)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "")
		}
	}
	return nil
}

func dreamCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("dream", flag.ContinueOnError)
	fs.SetOutput(stderr)
	journalPath := fs.String("journal", "", "measurement journal")
	outcomesPath := fs.String("outcomes", "", "optional outcomes jsonl")
	rulesPath := fs.String("rules", "", "active rules file, if one exists")
	proposals := fs.String("proposals", "rules/proposals", "directory for inactive proposals")
	bodies := fs.Bool("bodies", false, "opt in to reading the history store")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *journalPath == "" {
		fmt.Fprintln(stderr, "journal is required")
		return 2
	}
	records, err := metrics.ReadJournal(*journalPath)
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return 2
	}
	var outcomes []rules.Outcome
	if *outcomesPath != "" {
		outcomes, err = readOutcomes(*outcomesPath)
		if err != nil {
			fmt.Fprintln(stderr, err.Error())
			return 2
		}
	}
	var past []history.Record
	if *bodies {
		past, err = history.Read(history.PathFor(*journalPath))
		if err != nil {
			fmt.Fprintln(stderr, err.Error())
			return 2
		}
	}
	active, err := rules.Load(*rulesPath)
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return 2
	}
	proposal, path, err := rules.Dream(rules.DreamInput{
		Records: records, Outcomes: outcomes, History: past, Bodies: *bodies,
	}, active, *proposals)
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return 2
	}
	fmt.Fprintf(stdout, "wrote inactive proposal %s version %d with %d evidence rows\n", path, proposal.Version, len(proposal.Evidence))
	return 0
}

func rulesCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: conductor rules <diff|apply|rollback>")
		return 2
	}
	switch args[0] {
	case "diff":
		return rulesDiff(args[1:], stdout, stderr)
	case "apply":
		return rulesApply(args[1:], stderr)
	case "rollback":
		return rulesRollback(args[1:], stderr)
	default:
		fmt.Fprintf(stderr, "unknown rules command %s\n", args[0])
		return 2
	}
}

func rulesDiff(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("rules diff", flag.ContinueOnError)
	fs.SetOutput(stderr)
	activePath := fs.String("rules", "", "active rules file")
	proposalPath := fs.String("proposal", "", "proposal file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *proposalPath == "" {
		fmt.Fprintln(stderr, "proposal is required")
		return 2
	}
	active, err := rules.Load(*activePath)
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return 2
	}
	raw, err := os.ReadFile(*proposalPath)
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return 2
	}
	proposal, err := rules.ParseProposal(raw)
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return 2
	}
	fmt.Fprint(stdout, rules.Diff(active, proposal.File))
	return 0
}

func rulesApply(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("rules apply", flag.ContinueOnError)
	fs.SetOutput(stderr)
	activePath := fs.String("rules", "rules/active.json", "active rules file")
	proposalPath := fs.String("proposal", "", "proposal file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *proposalPath == "" {
		fmt.Fprintln(stderr, "proposal is required")
		return 2
	}
	if err := rules.Apply(*activePath, *proposalPath); err != nil {
		fmt.Fprintln(stderr, err.Error())
		return 2
	}
	return 0
}

func rulesRollback(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("rules rollback", flag.ContinueOnError)
	fs.SetOutput(stderr)
	activePath := fs.String("rules", "rules/active.json", "active rules file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := rules.Rollback(*activePath); err != nil {
		fmt.Fprintln(stderr, err.Error())
		return 2
	}
	return 0
}

func readOutcomes(path string) ([]rules.Outcome, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []rules.Outcome
	start := 0
	for i := 0; i <= len(raw); i++ {
		if i == len(raw) || raw[i] == '\n' {
			line := raw[start:i]
			start = i + 1
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			var outcome rules.Outcome
			if err := json.Unmarshal(line, &outcome); err != nil {
				return nil, err
			}
			out = append(out, outcome)
		}
	}
	return out, nil
}

func hopFrom(cfg config.Config) (aiuditor.Hop, error) {
	if cfg.AIU.Mode == config.AIUModeHTTP {
		return aiuditor.HTTP{URL: cfg.AIU.URL, Timeout: cfg.AIU.Timeout}, nil
	}
	rules, err := aiuditor.LoadRules(cfg.AIU.RulesFile)
	if err != nil {
		return nil, err
	}
	return aiuditor.Stub{Rules: rules}, nil
}

func load(configPath *string, stderr io.Writer) (config.Config, int) {
	if configPath == nil || *configPath == "" {
		fmt.Fprintln(stderr, "config is required")
		return config.Config{}, 2
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return config.Config{}, 2
	}
	return cfg, 0
}

func writePretty(stdout io.Writer, payload any) int {
	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(payload); err != nil {
		return 1
	}
	return 0
}
