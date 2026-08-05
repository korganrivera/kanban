package ops

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kanban-go/internal/board"
	"kanban-go/internal/store"
)

func createSourceDatabase(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	databasePath := filepath.Join(root, "live", "kanban.db")
	database, err := store.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	task, err := database.Create(context.Background(), board.TaskInput{Title: "Restore me"})
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	return databasePath, task.ID
}

func TestBackupVerifyAndRestoreDrill(t *testing.T) {
	databasePath, taskID := createSourceDatabase(t)
	destination := filepath.Join(filepath.Dir(filepath.Dir(databasePath)), "backups")
	result, err := CreateBackup(context.Background(), databasePath, destination, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Audit.OK || result.Audit.Counts["tasks"] != 1 {
		t.Fatalf("backup audit = %#v", result.Audit)
	}
	latestTarget, err := os.Readlink(filepath.Join(destination, "latest"))
	if err != nil {
		t.Fatal(err)
	}
	if latestTarget != filepath.Base(result.Directory) {
		t.Fatalf("latest target = %q, want %q", latestTarget, filepath.Base(result.Directory))
	}
	verified, err := VerifyBackup(context.Background(), filepath.Join(destination, "latest"))
	if err != nil {
		t.Fatal(err)
	}
	if verified.Manifest.Files[0].SHA256 != result.Manifest.Files[0].SHA256 {
		t.Fatalf("verified manifest = %#v", verified.Manifest)
	}

	restorePath := filepath.Join(t.TempDir(), "restored", "kanban.db")
	if err := copyFile(filepath.Join(result.Directory, "kanban.db"), restorePath); err != nil {
		t.Fatal(err)
	}
	restored, err := store.Open(restorePath)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if _, err := restored.Get(context.Background(), taskID); err != nil {
		t.Fatalf("restored task: %v", err)
	}
}

func TestVerifyRejectsCorruption(t *testing.T) {
	databasePath, _ := createSourceDatabase(t)
	destination := filepath.Join(filepath.Dir(filepath.Dir(databasePath)), "backups")
	result, err := CreateBackup(context.Background(), databasePath, destination, 2)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(filepath.Join(result.Directory, "kanban.db"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("corrupt")); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBackup(context.Background(), result.Directory); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("corrupt verification error = %v", err)
	}
}

func TestBackupRejectsLiveDataDestination(t *testing.T) {
	databasePath, _ := createSourceDatabase(t)
	if _, err := CreateBackup(context.Background(), databasePath, filepath.Join(filepath.Dir(databasePath), "backups"), 2); err == nil {
		t.Fatal("expected a backup inside the live data directory to be rejected")
	}
}

func TestPruneBackupsRetainsNewestRecognizedDirectories(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{
		"kanban-20260801T000000.000000000Z",
		"kanban-20260802T000000.000000000Z",
		"kanban-20260803T000000.000000000Z",
		"unrelated",
	} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := pruneBackups(root, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "kanban-20260801T000000.000000000Z")); !os.IsNotExist(err) {
		t.Fatalf("old backup was not pruned: %v", err)
	}
	for _, name := range []string{"kanban-20260802T000000.000000000Z", "kanban-20260803T000000.000000000Z", "unrelated"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("retained directory %s: %v", name, err)
		}
	}
}

func copyFile(source, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		return err
	}
	return output.Close()
}
