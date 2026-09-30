package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/multica-ai/multica/server/internal/centerrecovery"
	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

var recoveryCmd = newRecoveryCommand()

func recoveryRoot(cmd *cobra.Command) (string, error) {
	dir, err := cli.ProfileDir(resolveProfile(cmd))
	return filepath.Join(dir, "center-recovery"), err
}
func newRecoveryCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "recovery", Short: "Back up and restore a complete self-hosted center"}
	var source string
	cmd.PersistentFlags().StringVar(&source, "source", "", "Source center HTTP(S) origin (credentials stay scoped to this address)")
	sourceOrigin := func() (string, error) { return centerrecovery.Origin(source) }
	var allowHTTP bool
	configure := &cobra.Command{Use: "configure", Short: "Read the operator recovery token from stdin and enable hourly daemon backups", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		root, err := recoveryRoot(c)
		if err != nil {
			return err
		}
		origin, err := sourceOrigin()
		if err != nil {
			return err
		}
		raw, err := io.ReadAll(io.LimitReader(c.InOrStdin(), 4097))
		if err != nil {
			return err
		}
		if len(raw) > 4096 {
			return errors.New("recovery credential is too long")
		}
		if err = centerrecovery.Configure(root, centerrecovery.Source{Origin: origin, Token: strings.TrimSpace(string(raw)), AllowHTTP: allowHTTP}); err != nil {
			return err
		}
		fmt.Fprintln(c.OutOrStdout(), "Recovery source saved. Run recovery pull to verify the first backup.")
		return nil
	}}
	configure.Flags().BoolVar(&allowHTTP, "allow-http", false, "Allow unencrypted transfer on a trusted private network")
	pull := &cobra.Command{Use: "pull", Short: "Save an encrypted snapshot from the configured source", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		root, err := recoveryRoot(c)
		if err != nil {
			return err
		}
		origin, err := sourceOrigin()
		if err != nil {
			return err
		}
		s, err := centerrecovery.LoadSource(root, origin)
		if err != nil {
			return err
		}
		path, err := centerrecovery.Pull(c.Context(), root, s)
		if err != nil {
			return err
		}
		fmt.Fprintln(c.OutOrStdout(), path)
		return nil
	}}
	list := &cobra.Command{Use: "list", Short: "List verified snapshots retained across center changes", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		root, err := recoveryRoot(c)
		if err != nil {
			return err
		}
		files, err := filepath.Glob(filepath.Join(root, "*", "*.mcr"))
		if err != nil {
			return err
		}
		if len(files) == 0 {
			fmt.Fprintln(c.OutOrStdout(), "No center backups exist in this profile. Daemon execution caches are not a full center backup.")
			return nil
		}
		for _, file := range files {
			_, m, e := centerrecovery.Read(root, file)
			if e != nil {
				fmt.Fprintf(c.OutOrStdout(), "UNREADABLE\t%s\n", file)
				continue
			}
			fmt.Fprintf(c.OutOrStdout(), "%s\t%s\t%s\n", m.CenterID, m.CreatedAt.Format("2006-01-02T15:04:05Z"), file)
		}
		return nil
	}}
	var archive, directory, confirm, pgRestore string
	restore := &cobra.Command{Use: "restore", Short: "Restore to an offline empty database using RECOVERY_DATABASE_URL", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		if archive == "" || directory == "" || confirm == "" {
			return errors.New("--file, --directory and --confirm-center are required")
		}
		root, err := recoveryRoot(c)
		if err != nil {
			return err
		}
		data, _, err := centerrecovery.Read(root, archive)
		if err != nil {
			return err
		}
		if err = centerrecovery.Restore(c.Context(), data, directory, os.Getenv("RECOVERY_DATABASE_URL"), confirm, pgRestore); err != nil {
			return err
		}
		fmt.Fprintln(c.OutOrStdout(), "Database restored. Keep the old center stopped. Configure the replacement to use this database, the extracted uploads and deployment-keys.json before starting it. Sign in again on the replacement.")
		return nil
	}}
	restore.Flags().StringVar(&archive, "file", "", "Encrypted .mcr snapshot")
	restore.Flags().StringVar(&directory, "directory", "", "New extraction directory; must not exist")
	restore.Flags().StringVar(&confirm, "confirm-center", "", "Expected source center ID from recovery list")
	restore.Flags().StringVar(&pgRestore, "pg-restore", "", "Path to pg_restore (default: PATH)")
	var center, uploads, pgDump string
	capture := &cobra.Command{Use: "capture", Short: "Back up this machine's center using DATABASE_URL and deployment-key environment variables", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		if err := centerrecovery.ApplyActivation(centerrecovery.StateDir()); err != nil {
			return err
		}
		root, err := recoveryRoot(c)
		if err != nil {
			return err
		}
		origin, err := sourceOrigin()
		if err != nil {
			return err
		}
		if os.Getenv("S3_BUCKET") != "" {
			return errors.New("capture supports local uploads only; archive S3 storage separately")
		}
		keys := map[string]string{}
		for _, key := range centerrecovery.DeploymentKeys {
			keys[key] = os.Getenv(key)
		}
		data, err := centerrecovery.Capture(c.Context(), centerrecovery.CaptureOptions{CenterID: center, ServerVersion: version, DatabaseURL: os.Getenv("DATABASE_URL"), UploadDir: uploads, PGDump: pgDump, Secrets: keys})
		if err != nil {
			return err
		}
		path, err := centerrecovery.Save(root, origin, data)
		if err != nil {
			return err
		}
		fmt.Fprintln(c.OutOrStdout(), path)
		return nil
	}}
	capture.Flags().StringVar(&center, "center-id", "", "Stable source center ID")
	capture.Flags().StringVar(&uploads, "uploads", "./data/uploads", "Source center local upload directory")
	capture.Flags().StringVar(&pgDump, "pg-dump", "", "Path to pg_dump (default: PATH)")
	cmd.AddCommand(configure, pull, list, restore, capture)
	return cmd
}
