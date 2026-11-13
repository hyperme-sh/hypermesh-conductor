package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/hyperme-sh/hypermesh-conductor/internal/capabilitydispatch"
)

func capabilityDispatchCmd(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("capability-dispatch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "owned host capability configuration")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if *configPath == "" || fs.NArg() != 0 {
		return 2
	}
	call, err := capabilitydispatch.ReadCall(stdin)
	if err != nil {
		fmt.Fprintln(stderr, "capability dispatch refused")
		return 1
	}
	config, err := capabilitydispatch.Load(*configPath)
	if err != nil {
		fmt.Fprintln(stderr, "capability dispatch refused")
		return 1
	}
	dispatcher, close, err := capabilitydispatch.Open(config)
	if err != nil {
		fmt.Fprintln(stderr, "capability dispatch refused")
		return 1
	}
	defer close()
	result, err := dispatcher.Dispatch(context.Background(), call)
	if err != nil {
		fmt.Fprintln(stderr, "capability dispatch refused")
		return 1
	}
	return writePretty(stdout, result)
}

func capabilityCatalogCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("capability-catalog", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("config", "", "owned host capability configuration")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if *path == "" || fs.NArg() != 0 {
		return 2
	}
	config, err := capabilitydispatch.Load(*path)
	if err != nil {
		fmt.Fprintln(stderr, "capability catalog refused")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entries, err := capabilitydispatch.List(ctx, config)
	if err != nil {
		fmt.Fprintln(stderr, "capability catalog refused")
		return 1
	}
	return writePretty(stdout, entries)
}
