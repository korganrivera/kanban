package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"kanban-go/internal/httpapi"
	"kanban-go/internal/store"
)

var version = "dev"

func main() {
	restrictProcessPermissions()
	if err := run(os.Args[1:]); err != nil {
		reportFatal(err)
		os.Exit(1)
	}
}

func run(args []string) error {
	address := envOr("KANBAN_ADDR", "127.0.0.1:3100")
	dataDir := envOr("KANBAN_DATA_DIR", defaultDataDir())
	boardURL := serverURL(address)
	background := hasArgument(args, "--background")

	if desktopIntegration() && kanbanHealthy(boardURL, 400*time.Millisecond) {
		if background {
			return nil
		}
		return openBrowser(boardURL)
	}

	closeLog, err := configureProcess(dataDir)
	if err != nil {
		return fmtError("configure logging", err)
	}
	defer closeLog()
	log.Printf("Kanban %s starting", version)

	database, err := store.Open(filepath.Join(dataDir, "kanban.db"))
	if err != nil {
		return fmtError("open database", err)
	}
	defer database.Close()
	stopBackups := startAutomaticBackups(dataDir)
	defer stopBackups()

	application := httpapi.New(database, httpapi.Config{
		AuthEnabled:       true,
		AllowRegistration: envBool("ALLOW_REGISTRATION"),
		CookieSecure:      envBool("COOKIE_SECURE"),
	})
	httpServer := &http.Server{
		Addr:              address,
		Handler:           application.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmtError("listen on "+address, err)
	}

	serverErrors := make(chan error, 1)
	go func() {
		log.Printf("Kanban Go listening on http://%s", address)
		if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
		}
	}()

	if desktopIntegration() && !background {
		if kanbanHealthy(boardURL, 5*time.Second) {
			if err := openBrowser(boardURL); err != nil {
				log.Printf("open browser: %v", err)
			}
		} else {
			log.Printf("Kanban did not become ready before the browser-open timeout")
		}
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, terminationSignals()...)
	select {
	case err := <-serverErrors:
		application.Close()
		return fmtError("serve Kanban", err)
	case <-stop:
	}
	application.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		return fmtError("shutdown", err)
	}
	return nil
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envBool(name string) bool {
	switch os.Getenv(name) {
	case "1", "true", "TRUE", "yes", "YES", "on", "ON":
		return true
	default:
		return false
	}
}

func hasArgument(args []string, wanted string) bool {
	for _, argument := range args {
		if argument == wanted {
			return true
		}
	}
	return false
}

func serverURL(address string) string {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "http://127.0.0.1:3100"
	}
	switch strings.Trim(host, "[]") {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

func kanbanHealthy(baseURL string, timeout time.Duration) bool {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	deadline := time.Now().Add(timeout)
	for {
		response, err := client.Get(strings.TrimRight(baseURL, "/") + "/healthz")
		if err == nil {
			var status struct {
				Status string `json:"status"`
			}
			decodeErr := json.NewDecoder(response.Body).Decode(&status)
			response.Body.Close()
			if response.StatusCode == http.StatusOK && decodeErr == nil && status.Status == "ok" {
				return true
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(75 * time.Millisecond)
	}
}

func fmtError(action string, err error) error {
	return errors.New(action + ": " + err.Error())
}
