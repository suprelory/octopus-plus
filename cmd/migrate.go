package cmd

import (
	"encoding/json"
	"errors"

	"github.com/spf13/cobra"
	v2 "github.com/suprelory/octopus-plus/internal/migration/v2"
)

func newMigrationCommand() *cobra.Command {
	command := &cobra.Command{Use: "migrate", Short: "Inspect and migrate data for a version upgrade"}
	var input string
	preflight := &cobra.Command{
		Use: "v2-preflight", Short: "Inspect a v1 JSON or ZIP backup without modifying data",
		Args: cobra.NoArgs, SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			snapshot, err := v2.ReadBackup(input)
			if err != nil {
				return err
			}
			report, err := v2.Analyze(snapshot)
			if err != nil {
				return err
			}
			encoder := json.NewEncoder(cmd.OutOrStdout())
			encoder.SetIndent("", "  ")
			if err := encoder.Encode(report); err != nil {
				return errors.New("cannot write preflight report")
			}
			if report.Summary.Blockers > 0 {
				return errors.New("preflight found blocking issues; inspect the report")
			}
			return nil
		},
	}
	preflight.Flags().StringVar(&input, "input", "", "Path to an existing version 1 JSON or ZIP backup")
	_ = preflight.MarkFlagRequired("input")
	command.AddCommand(preflight, newDatabaseMigrationCommand())
	return command
}

func init() {
	rootCmd.AddCommand(newMigrationCommand())
}
