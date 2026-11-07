package main

import (
	"fmt"
	"os"

	"github.com/suprelory/octopus-plus/cmd"
	"github.com/suprelory/octopus-plus/internal/update"
)

func main() {
	handled, err := update.RunWindowsUpdateHelper(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if handled {
		return
	}
	if update.WindowsUpdateCleanupPending() {
		go func() {
			if err := update.CleanupWindowsUpdate(); err != nil {
				fmt.Fprintln(os.Stderr, err)
			}
		}()
	}
	cmd.Execute()
}
