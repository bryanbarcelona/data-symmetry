package main

import (
	"os"

	"github.com/spf13/cobra"
	"github.com/bryanbarcelona/data-symmetry/internal/junksweep"
	"github.com/bryanbarcelona/data-symmetry/internal/twincheck"
)

func main() {
	root := &cobra.Command{Use: "ds"}
	root.AddCommand(junksweep.Cmd)
	root.AddCommand(twincheck.Cmd)
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}