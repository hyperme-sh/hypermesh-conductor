package main

import (
	"os"

	"github.com/hyperme-sh/hypermesh-conductor/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
}
