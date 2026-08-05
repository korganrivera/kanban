package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"kanban-go/internal/ops"
	"kanban-go/internal/store"
)

func main() {
	syscall.Umask(0o077)
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usageError()
	}
	switch args[0] {
	case "backup":
		return runBackup(args[1:])
	case "verify":
		return runVerify(args[1:])
	case "audit":
		return runAudit(args[1:])
	case "help", "-h", "--help":
		fmt.Println(usageText)
		return nil
	default:
		return usageError()
	}
}

func runBackup(args []string) error {
	flags := flag.NewFlagSet("backup", flag.ContinueOnError)
	dataDir := flags.String("data-dir", envOr("KANBAN_DATA_DIR", "data"), "kanban data directory")
	destination := flags.String("destination", os.Getenv("KANBAN_BACKUP_DEST"), "dedicated backup directory")
	defaultRetention, err := envInt("KANBAN_BACKUP_RETENTION", 30)
	if err != nil {
		return err
	}
	retention := flags.Int("retention", defaultRetention, "number of backups to retain")
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *destination == "" {
		return errors.New("--destination or KANBAN_BACKUP_DEST is required")
	}
	result, err := ops.CreateBackup(
		context.Background(), filepath.Join(*dataDir, "kanban.db"), *destination, *retention,
	)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(result)
	}
	fmt.Printf("Backup: %s\nSHA-256: %s\nTasks: %d\n",
		result.Directory, result.Manifest.Files[0].SHA256, result.Audit.Counts["tasks"],
	)
	return nil
}

func runVerify(args []string) error {
	flags := flag.NewFlagSet("verify", flag.ContinueOnError)
	backupDirectory := flags.String("backup", "", "backup directory or latest symlink")
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *backupDirectory == "" {
		return errors.New("--backup is required")
	}
	result, err := ops.VerifyBackup(context.Background(), *backupDirectory)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(result)
	}
	fmt.Printf("Verified: %s\nSHA-256: %s\nTasks: %d\n",
		result.Directory, result.Manifest.Files[0].SHA256, result.Audit.Counts["tasks"],
	)
	return nil
}

func runAudit(args []string) error {
	flags := flag.NewFlagSet("audit", flag.ContinueOnError)
	dataDir := flags.String("data-dir", envOr("KANBAN_DATA_DIR", "data"), "kanban data directory")
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	report, err := store.AuditPath(context.Background(), filepath.Join(*dataDir, "kanban.db"))
	if err != nil {
		return err
	}
	if *asJSON {
		if err := printJSON(report); err != nil {
			return err
		}
	} else {
		fmt.Printf("OK: %t\nSchema: %d\nTasks: %d\nUsers: %d\nCompletion entries: %d\n",
			report.OK, report.SchemaVersion, report.Counts["tasks"], report.Counts["users"], report.Counts["completionEntries"],
		)
		for _, finding := range report.Findings {
			fmt.Printf("Finding: %s\n", finding)
		}
	}
	if !report.OK {
		return errors.New("database audit failed")
	}
	return nil
}

func printJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envInt(name string, fallback int) (int, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return parsed, nil
}

func usageError() error {
	return errors.New(usageText)
}

const usageText = `Usage: kanban-admin <command> [options]

Commands:
  backup  Create, verify, publish, and prune a consistent SQLite backup
  verify  Verify a backup checksum and database invariants
  audit   Audit the live database without modifying it`
