package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"kanban-go/internal/store"
)

const backupTimeLayout = "20060102T150405.000000000Z"

type Manifest struct {
	Version        int            `json:"version"`
	CreatedAt      time.Time      `json:"createdAt"`
	SourceDatabase string         `json:"sourceDatabase"`
	Files          []ManifestFile `json:"files"`
}

type ManifestFile struct {
	Name   string `json:"name"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type BackupResult struct {
	Directory string             `json:"directory"`
	Manifest  Manifest           `json:"manifest"`
	Audit     *store.AuditReport `json:"audit"`
}

type VerifyResult struct {
	Directory string             `json:"directory"`
	Manifest  Manifest           `json:"manifest"`
	Audit     *store.AuditReport `json:"audit"`
}

func CreateBackup(ctx context.Context, databasePath, destinationRoot string, retention int) (*BackupResult, error) {
	if retention < 2 || retention > 1000 {
		return nil, errors.New("backup retention must be between 2 and 1000")
	}
	databasePath, err := filepath.Abs(databasePath)
	if err != nil {
		return nil, err
	}
	destinationRoot, err = filepath.Abs(destinationRoot)
	if err != nil {
		return nil, err
	}
	if err := validateBackupPaths(databasePath, destinationRoot); err != nil {
		return nil, err
	}
	info, err := os.Stat(databasePath)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("live database is not a regular file")
	}
	if err := os.MkdirAll(destinationRoot, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(destinationRoot, 0o700); err != nil {
		return nil, err
	}

	createdAt := time.Now().UTC()
	name := "kanban-" + createdAt.Format(backupTimeLayout)
	finalDirectory := filepath.Join(destinationRoot, name)
	tempDirectory := filepath.Join(destinationRoot, fmt.Sprintf(".%s.tmp-%d", name, os.Getpid()))
	if err := os.Mkdir(tempDirectory, 0o700); err != nil {
		return nil, err
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(tempDirectory)
		}
	}()

	database, err := store.Open(databasePath)
	if err != nil {
		return nil, err
	}
	backupDatabase := filepath.Join(tempDirectory, "kanban.db")
	if err := database.BackupTo(ctx, backupDatabase); err != nil {
		database.Close()
		return nil, err
	}
	if err := database.Close(); err != nil {
		return nil, err
	}
	audit, err := store.AuditPath(ctx, backupDatabase)
	if err != nil {
		return nil, err
	}
	if !audit.OK {
		return nil, fmt.Errorf("backup database failed audit: %s", strings.Join(audit.Findings, "; "))
	}

	digest, bytes, err := hashFile(backupDatabase)
	if err != nil {
		return nil, err
	}
	manifest := Manifest{
		Version: 1, CreatedAt: createdAt, SourceDatabase: databasePath,
		Files: []ManifestFile{{Name: "kanban.db", Bytes: bytes, SHA256: digest}},
	}
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	manifestData = append(manifestData, '\n')
	manifestPath := filepath.Join(tempDirectory, "manifest.json")
	if err := writeExclusive(manifestPath, manifestData, 0o600); err != nil {
		return nil, err
	}
	if err := syncPath(backupDatabase); err != nil {
		return nil, err
	}
	if err := syncPath(manifestPath); err != nil {
		return nil, err
	}
	if err := syncPath(tempDirectory); err != nil {
		return nil, err
	}
	if err := os.Rename(tempDirectory, finalDirectory); err != nil {
		return nil, err
	}
	published = true
	if err := updateLatest(destinationRoot, name); err != nil {
		return nil, err
	}
	if err := pruneBackups(destinationRoot, retention); err != nil {
		return nil, err
	}
	if err := syncPath(destinationRoot); err != nil {
		return nil, err
	}
	return &BackupResult{Directory: finalDirectory, Manifest: manifest, Audit: audit}, nil
}

func VerifyBackup(ctx context.Context, backupDirectory string) (*VerifyResult, error) {
	backupDirectory, err := filepath.Abs(backupDirectory)
	if err != nil {
		return nil, err
	}
	manifestData, err := os.ReadFile(filepath.Join(backupDirectory, "manifest.json"))
	if err != nil {
		return nil, err
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return nil, fmt.Errorf("parse backup manifest: %w", err)
	}
	if manifest.Version != 1 || len(manifest.Files) != 1 || manifest.Files[0].Name != "kanban.db" {
		return nil, errors.New("unsupported backup manifest")
	}
	entry := manifest.Files[0]
	if filepath.Base(entry.Name) != entry.Name {
		return nil, errors.New("backup manifest contains an unsafe file name")
	}
	databasePath := filepath.Join(backupDirectory, entry.Name)
	digest, bytes, err := hashFile(databasePath)
	if err != nil {
		return nil, err
	}
	if digest != entry.SHA256 || bytes != entry.Bytes {
		return nil, fmt.Errorf("backup checksum failed for %s", entry.Name)
	}
	audit, err := store.AuditPath(ctx, databasePath)
	if err != nil {
		return nil, err
	}
	if !audit.OK {
		return nil, fmt.Errorf("backup database failed audit: %s", strings.Join(audit.Findings, "; "))
	}
	return &VerifyResult{Directory: backupDirectory, Manifest: manifest, Audit: audit}, nil
}

func validateBackupPaths(databasePath, destinationRoot string) error {
	root := filepath.VolumeName(destinationRoot) + string(filepath.Separator)
	if filepath.Clean(destinationRoot) == filepath.Clean(root) {
		return errors.New("backup destination cannot be a filesystem root")
	}
	liveDirectory := filepath.Dir(databasePath)
	if pathWithin(destinationRoot, liveDirectory) || pathWithin(liveDirectory, destinationRoot) {
		return errors.New("backup destination must be a dedicated directory outside the live data directory")
	}
	return nil
}

func pathWithin(path, parent string) bool {
	relative, err := filepath.Rel(parent, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func hashFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	bytes, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), bytes, nil
}

func writeExclusive(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func pruneBackups(root string, retention int) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	backups := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "kanban-") {
			continue
		}
		if _, err := time.Parse(backupTimeLayout, strings.TrimPrefix(entry.Name(), "kanban-")); err != nil {
			continue
		}
		backups = append(backups, entry.Name())
	}
	sort.Sort(sort.Reverse(sort.StringSlice(backups)))
	if len(backups) <= retention {
		return nil
	}
	for _, stale := range backups[retention:] {
		if err := os.RemoveAll(filepath.Join(root, stale)); err != nil {
			return err
		}
	}
	return nil
}
