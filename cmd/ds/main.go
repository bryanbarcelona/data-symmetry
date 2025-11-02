package main

import (
	"os"

	"github.com/bryanbarcelona/data-symmetry/internal/build"
	"github.com/bryanbarcelona/data-symmetry/internal/junksweep"
	"github.com/bryanbarcelona/data-symmetry/internal/twincheck"
	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{Use: "ds"}
	root.Version = build.Version
	root.AddCommand(junksweep.Cmd)
	root.AddCommand(twincheck.Cmd)
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
