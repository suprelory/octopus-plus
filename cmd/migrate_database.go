package cmd

import (
	"encoding/json"
	"errors"
	"os"

	"github.com/spf13/cobra"
	"github.com/suprelory/octopus-plus/internal/conf"
	"github.com/suprelory/octopus-plus/internal/db"
	v2 "github.com/suprelory/octopus-plus/internal/migration/v2"
)

func newDatabaseMigrationCommand() *cobra.Command {
	var snapshot, backup, config string
	var apply, stopped bool
	cmd := &cobra.Command{Use: "v2-database", Short: "Preview, apply or resume an offline v1 database migration", Args: cobra.NoArgs, SilenceUsage: true}
	cmd.Flags().StringVar(&snapshot, "snapshot", "", "Private checkpoint file to create during preview and reuse during apply or resume")
	cmd.Flags().StringVar(&config, "config", "", "Application config path")
	cmd.Flags().BoolVar(&apply, "apply", false, "Write the migration using an existing checkpoint")
	cmd.Flags().BoolVar(&stopped, "writers-stopped", false, "Confirm all application writers have stopped")
	cmd.Flags().StringVar(&backup, "backup", "", "Existing full database backup for rollback")
	_ = cmd.MarkFlagRequired("snapshot")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		settings, err := conf.ReadDatabaseConfig(config)
		if err != nil {
			return errors.New("cannot load application config")
		}
		dialect, dsn := settings.Type, settings.Path
		if dsn == "" {
			return errors.New("configured migration database path is empty")
		}
		var live os.FileInfo
		if dialect == "sqlite" {
			live, err = os.Stat(dsn)
			if err != nil || !live.Mode().IsRegular() {
				return errors.New("migration requires an existing SQLite database file")
			}
		}
		snapInfo, statErr := os.Stat(snapshot)
		if statErr != nil && !os.IsNotExist(statErr) {
			return errors.New("cannot access migration checkpoint")
		}
		if snapInfo != nil && (!snapInfo.Mode().IsRegular() || live != nil && os.SameFile(live, snapInfo)) {
			return errors.New("invalid checkpoint file")
		}
		if apply {
			if !stopped || snapInfo == nil {
				return errors.New("apply requires a prepared checkpoint and --writers-stopped")
			}
			info, err := os.Stat(backup)
			if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || os.SameFile(info, snapInfo) || live != nil && os.SameFile(info, live) {
				return errors.New("apply requires a separate, nonempty full database backup file")
			}
		}
		conn, err := db.OpenMigrationConnection(dialect, dsn, !apply)
		if err != nil {
			return errors.New("cannot open migration database")
		}
		pool, err := conn.DB()
		if err != nil {
			return errors.New("cannot access migration database")
		}
		defer pool.Close()
		var input *v2.LegacyInput
		if snapInfo == nil {
			input, err = v2.CaptureDatabaseInput(cmd.Context(), conn)
			if err != nil {
				return err
			}
			if err = v2.WriteSnapshotFile(snapshot, v2.DatabaseSource, input); err != nil {
				return err
			}
		} else {
			var source string
			source, input, err = v2.ReadSnapshotFile(snapshot)
			if err != nil {
				return err
			}
			if source != v2.DatabaseSource {
				return errors.New("checkpoint belongs to another migration source")
			}
		}
		migrate := v2.InspectDatabase
		if apply {
			migrate = v2.MigrateDatabase
		}
		result, err := migrate(cmd.Context(), conn, v2.DatabaseSource, input)
		if result != nil {
			if writeErr := json.NewEncoder(cmd.OutOrStdout()).Encode(result); writeErr != nil {
				return errors.New("cannot write migration report")
			}
		}
		return err
	}
	return cmd
}
