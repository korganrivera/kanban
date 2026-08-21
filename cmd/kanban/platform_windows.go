//go:build windows

package main

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"
	"unsafe"

	"kanban-go/internal/ops"

	"golang.org/x/sys/windows"
)

func restrictProcessPermissions() {}

func defaultDataDir() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base, _ = os.UserConfigDir()
	}
	if base == "" {
		return "data"
	}
	return filepath.Join(base, "Kanban", "data")
}

func desktopIntegration() bool {
	return true
}

func configureProcess(dataDir string) (func(), error) {
	stateDir := filepath.Dir(dataDir)
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, err
	}
	logFile, err := os.OpenFile(
		filepath.Join(stateDir, "kanban.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY,
		0o600,
	)
	if err != nil {
		return nil, err
	}
	log.SetOutput(logFile)
	return func() { _ = logFile.Close() }, nil
}

func startAutomaticBackups(dataDir string) func() {
	backupContext, cancel := context.WithCancel(context.Background())
	databasePath := filepath.Join(dataDir, "kanban.db")
	destination := filepath.Join(filepath.Dir(dataDir), "backups")
	runBackup := func() {
		result, err := ops.CreateBackup(backupContext, databasePath, destination, 30)
		if err != nil {
			log.Printf("automatic backup: %v", err)
			return
		}
		log.Printf("automatic backup: %s", result.Directory)
	}
	go func() {
		runBackup()
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				runBackup()
			case <-backupContext.Done():
				return
			}
		}
	}()
	return cancel
}

func openBrowser(boardURL string) error {
	return exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", boardURL).Start()
}

func reportFatal(err error) {
	log.Printf("Kanban: %v", err)
	messageBox("Kanban could not start.\n\n"+err.Error(), "Kanban", 0x10)
}

func terminationSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}

func messageBox(message, title string, flags uintptr) {
	messagePointer, messageErr := windows.UTF16PtrFromString(message)
	titlePointer, titleErr := windows.UTF16PtrFromString(title)
	if messageErr != nil || titleErr != nil {
		return
	}
	procedure := windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW")
	_, _, _ = procedure.Call(
		0,
		uintptr(unsafe.Pointer(messagePointer)),
		uintptr(unsafe.Pointer(titlePointer)),
		flags,
	)
}
