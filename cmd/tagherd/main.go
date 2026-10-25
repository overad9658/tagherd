// Command tagherd scans cloud resources against a tagging policy and fixes their tags.
package main

import (
	"os"

	"github.com/overad9658/tagherd/internal/cli"
)

func main() {
	os.Exit(cli.Execute(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.LookupEnv))
}
