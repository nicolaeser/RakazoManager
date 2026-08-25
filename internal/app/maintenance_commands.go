package app

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/nicolaeser/RakazoManager/internal/manager"
	"github.com/nicolaeser/RakazoManager/internal/ui"
)

func (app *App) dashboardCLICommand(_ context.Context, terminal *ui.UI, args []string) error {
	root, err := oneFolder(args)
	if err != nil {
		return fmt.Errorf("usage: rakazo-manager open [FOLDER]")
	}
	rt, err := app.runtime(root, terminal)
	if err != nil {
		return err
	}
	return rt.dashboardCommand()
}

func (app *App) readSecretFile(path string) (string, error) {
	var raw []byte
	var err error
	if path == "-" {
		raw, err = io.ReadAll(app.In)
	} else {
		raw, err = os.ReadFile(path)
	}
	if err != nil {
		return "", fmt.Errorf("read secret file: %w", err)
	}
	value := strings.TrimRight(string(raw), "\r\n")
	if strings.ContainsAny(value, "\x00") {
		return "", fmt.Errorf("secret must be a single line without null characters")
	}
	return value, nil
}

func (app *App) backupsCommand(ctx context.Context, terminal *ui.UI, args []string) error {
	if len(args) == 0 || args[0] != "prune" {
		flags := flag.NewFlagSet("backups", flag.ContinueOnError)
		flags.SetOutput(app.Err)
		jsonOutput := flags.Bool("json", false, "write stable machine-readable JSON")
		if err := flags.Parse(args); err != nil {
			return err
		}
		root, err := oneFolder(flags.Args())
		if err != nil {
			return fmt.Errorf("usage: rakazo-manager backups [--json] [FOLDER]")
		}
		rt, err := app.runtime(root, terminal)
		if err != nil {
			return err
		}
		if *jsonOutput {
			files, listErr := rt.manager.ListBackups()
			if listErr != nil {
				return listErr
			}
			return writeBackupsJSON(app.Out, files)
		}
		return rt.listBackups()
	}
	flags := flag.NewFlagSet("backups prune", flag.ContinueOnError)
	flags.SetOutput(app.Err)
	keep := flags.Int("keep", 10, "number of newest automatic safety backups to retain")
	dryRun := flags.Bool("dry-run", false, "list automatic backups that would be deleted without deleting them")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	root, err := oneFolder(flags.Args())
	if err != nil {
		return fmt.Errorf("usage: rakazo-manager backups prune [--keep N] [--dry-run] [FOLDER]")
	}
	rt, err := app.runtime(root, terminal)
	if err != nil {
		return err
	}
	files, err := rt.manager.BackupsToPrune(*keep)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		terminal.Success("Nothing to prune; manual backups and instance exports are never selected")
		return nil
	}
	terminal.Section("Automatic backups to delete")
	for _, file := range files {
		fmt.Fprintln(app.Out, "  "+file)
	}
	if *dryRun {
		terminal.Success("Dry run complete; no backups were deleted")
		return nil
	}
	confirmed, err := terminal.Confirm(fmt.Sprintf("Delete %d old automatic safety backup(s)?", len(files)), false)
	if err != nil {
		return err
	}
	if !confirmed {
		return fmt.Errorf("backup pruning cancelled")
	}
	deleted, err := rt.manager.PruneAutomaticBackupsExpected(*keep, files)
	if err != nil {
		return err
	}
	terminal.Success("Deleted %d old automatic backup(s); manual backups were retained", len(deleted))
	return nil
}

func (app *App) exportInstanceCommand(ctx context.Context, terminal *ui.UI, args []string) error {
	flags := flag.NewFlagSet("export-instance", flag.ContinueOnError)
	flags.SetOutput(app.Err)
	withoutWorkspace := flags.Bool("without-workspace", false, "exclude workspace files")
	if err := flags.Parse(args); err != nil {
		return err
	}
	root, err := oneFolder(flags.Args())
	if err != nil {
		return fmt.Errorf("usage: rakazo-manager export-instance [--without-workspace] [FOLDER]")
	}
	rt, err := app.runtime(root, terminal)
	if err != nil {
		return err
	}
	confirmed, err := terminal.Confirm("Create a disaster-recovery export containing Postgres, data, and secrets?", true)
	if err != nil {
		return err
	}
	if !confirmed {
		return fmt.Errorf("instance export cancelled")
	}
	archive, err := rt.manager.ExportInstance(ctx, !*withoutWorkspace)
	if err != nil {
		return err
	}
	terminal.Success("Instance export created and verified: %s", archive)
	terminal.Warn("This archive contains secrets; store it securely.")
	return nil
}

func (app *App) doctorCommand(ctx context.Context, terminal *ui.UI, args []string) error {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(app.Err)
	jsonOutput := flags.Bool("json", false, "write stable machine-readable JSON")
	clearStaleLock := flags.Bool("clear-stale-lock", false, "remove a leftover operation lock only when its process is not alive")
	if err := flags.Parse(args); err != nil {
		return err
	}
	root, err := oneFolder(flags.Args())
	if err != nil {
		return fmt.Errorf("usage: rakazo-manager doctor [--json] [--clear-stale-lock] [FOLDER]")
	}
	rt, err := app.runtime(root, terminal)
	if err != nil {
		return err
	}
	if *clearStaleLock {
		if *jsonOutput {
			return fmt.Errorf("--json cannot be combined with --clear-stale-lock")
		}
		cleared, detail, clearErr := rt.manager.ClearStaleLock()
		if clearErr != nil {
			return clearErr
		}
		if cleared {
			terminal.Success("Cleared stale operation lock")
			terminal.KeyValue("Previous lock", detail)
		} else {
			terminal.Success("No operation lock needed clearing")
			terminal.Info("%s", detail)
		}
	}
	report := rt.manager.Doctor(ctx)
	if *jsonOutput {
		if err := writeDoctorJSON(app.Out, report); err != nil {
			return err
		}
	} else {
		rt.printDoctor(report)
	}
	if !report.Healthy() {
		return fmt.Errorf("one or more doctor checks failed")
	}
	return nil
}

func (rt runtime) printDoctor(report manager.DoctorReport) {
	rt.ui.Section("Rakazo Manager doctor")
	for _, check := range report.Checks {
		fmt.Fprintf(rt.app.Out, "  %-4s  %-24s %s\n", check.Level, check.Name, check.Detail)
	}
}
