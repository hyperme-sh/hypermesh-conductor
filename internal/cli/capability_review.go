package cli

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/hyperme-sh/hypermesh-conductor/internal/capabilityreview"
)

func capabilityReviewCmd(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("capability-review", flag.ContinueOnError)
	fs.SetOutput(stderr)
	endpoint := fs.String("endpoint", "", "trusted AIuditor HTTPS capability-review endpoint")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *endpoint == "" {
		return 2
	}
	raw, err := io.ReadAll(io.LimitReader(stdin, 16385))
	if err != nil || len(raw) > 16384 {
		fmt.Fprintln(stderr, "capability review refused")
		return 1
	}
	result, err := (capabilityreview.Client{Endpoint: *endpoint}).Review(context.Background(), raw)
	if err != nil {
		fmt.Fprintln(stderr, "capability review refused")
		return 1
	}
	if code := writePretty(stdout, result); code != 0 {
		return code
	}
	if result.Decision != "allow" {
		return 1
	}
	return 0
}
