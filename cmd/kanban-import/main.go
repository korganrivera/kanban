package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"kanban-go/internal/legacy"
	"kanban-go/internal/store"
)

type output struct {
	Mode       string                    `json:"mode"`
	Source     string                    `json:"source"`
	Database   string                    `json:"database,omitempty"`
	Backup     string                    `json:"backup,omitempty"`
	SourceData legacy.Report             `json:"sourceData"`
	Imported   *store.LegacyImportReport `json:"imported,omitempty"`
}

func main() {
	syscall.Umask(0o077)
	log.SetFlags(0)
	var sourceDir string
	var dataDir string
	var apply bool
	var replace bool
	var asJSON bool
	flag.StringVar(&sourceDir, "source", "", "legacy directory containing tasks.json, users.json, and wip_limits.json")
	flag.StringVar(&dataDir, "data-dir", "data", "Go kanban data directory")
	flag.BoolVar(&apply, "apply", false, "write the validated import")
	flag.BoolVar(&replace, "replace", false, "replace existing destination data (requires --apply)")
	flag.BoolVar(&asJSON, "json", false, "print a JSON report")
	flag.Parse()
	if sourceDir == "" {
		log.Fatal("--source is required")
	}
	if replace && !apply {
		log.Fatal("--replace requires --apply")
	}

	bundle, err := legacy.Load(sourceDir)
	if err != nil {
		log.Fatalf("validate legacy data: %v", err)
	}
	result := output{Mode: "dry-run", Source: sourceDir, SourceData: bundle.Report}
	if apply {
		result.Mode = "applied"
		result.Database = filepath.Join(dataDir, "kanban.db")
		database, err := store.Open(result.Database)
		if err != nil {
			log.Fatal(err)
		}
		defer database.Close()

		backupName := "pre-import-" + time.Now().UTC().Format("20060102T150405.000000000Z") + ".db"
		result.Backup = filepath.Join(dataDir, "backups", backupName)
		ctx := context.Background()
		if err := database.BackupTo(ctx, result.Backup); err != nil {
			log.Fatalf("create pre-import backup: %v", err)
		}
		result.Imported, err = database.ImportLegacy(ctx, bundle, replace)
		if err != nil {
			log.Fatalf("import legacy data (backup retained at %s): %v", result.Backup, err)
		}
	}

	if asJSON {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(result); err != nil {
			log.Fatal(err)
		}
		return
	}
	printText(result)
}

func printText(result output) {
	fmt.Printf("Legacy import %s\n", result.Mode)
	fmt.Printf("Source: %s\n", result.Source)
	fmt.Printf("Tasks: %d\nUsers: %d\nPoint entries: %d\nPoint snapshots: %d\nUndo candidates: %d\n",
		result.SourceData.Tasks, result.SourceData.Users, result.SourceData.PointEntries,
		result.SourceData.PointSnapshots, result.SourceData.UndoCandidates,
	)
	if result.Imported != nil {
		fmt.Printf("Database: %s\nBackup: %s\nUndo imported: %d\nUndo skipped: %d\n",
			result.Database, result.Backup, result.Imported.UndoImported, result.Imported.UndoSkipped,
		)
	}
	warnings := result.SourceData.Warnings
	if result.Imported != nil {
		warnings = result.Imported.Warnings
	}
	for _, warning := range warnings {
		fmt.Printf("Warning: %s\n", warning)
	}
	if result.Mode == "dry-run" {
		fmt.Println("No destination data was written. Use --apply after reviewing this report.")
	}
}
