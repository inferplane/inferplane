package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/inferplane/inferplane/internal/config"
)

func configPathArgs(name string, args []string) (string, error) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("config", "config.json", "configuration path")
	if err := flags.Parse(args); err != nil {
		return "", err
	}
	if flags.NArg() != 0 {
		return "", fmt.Errorf("%s: unexpected argument %q", name, flags.Arg(0))
	}
	if *path == "" {
		return "", fmt.Errorf("%s: --config requires a nonempty path", name)
	}
	return *path, nil
}

// Validate the declaration without contacting providers or opening runtime
// stores. LoadRaw validates its secret references; provider construction and
// runtime readiness are checked by serve.
func configCmd(args []string) int {
	if len(args) == 0 || args[0] != "validate" {
		fmt.Fprintln(os.Stderr, "usage: mayu config validate --config <path>")
		return 2
	}
	path, err := configPathArgs("config validate", args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	if _, err := config.LoadRaw(path); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Fprintln(os.Stdout, "configuration valid")
	return 0
}
