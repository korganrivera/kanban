//go:build !windows

package main

import (
	"errors"
	"log"
	"os"
	"syscall"
)

func restrictProcessPermissions() {
	syscall.Umask(0o077)
}

func defaultDataDir() string {
	return "data"
}

func desktopIntegration() bool {
	return false
}

func configureProcess(string) (func(), error) {
	return func() {}, nil
}

func startAutomaticBackups(string) func() {
	return func() {}
}

func openBrowser(string) error {
	return errors.New("automatic browser opening is unavailable on this platform")
}

func reportFatal(err error) {
	log.Printf("Kanban: %v", err)
}

func terminationSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}
