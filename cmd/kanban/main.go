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
	"runtime"
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
	localActor := strings.TrimSpace(os.Getenv("KANBAN_LOCAL_ACTOR"))
	localSocketPath := configuredLocalSocket(dataDir, localActor)
	if localSocketPath != "" && localActor == "" {
		return errors.New("KANBAN_LOCAL_ACTOR must name a registered user when KANBAN_LOCAL_SOCKET is enabled")
	}
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
	serverErrors := make(chan error, 2)
	var localServer *http.Server
	if localSocketPath != "" {
		localListener, err := listenLocalSocket(localSocketPath)
		if err != nil {
			listener.Close()
			return fmtError("listen on local socket "+localSocketPath, err)
		}
		defer os.Remove(localSocketPath)
		localServer = &http.Server{
			Handler:           application.TrustedHandler(localActor),
			ReadHeaderTimeout: 5 * time.Second,
			IdleTimeout:       60 * time.Second,
		}
		go func() {
			log.Printf("Trusted local API listening on unix://%s as %s", localSocketPath, localActor)
			if err := localServer.Serve(localListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				serverErrors <- err
			}
		}()
	}

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
	var serveErr error
	select {
	case err := <-serverErrors:
		serveErr = err
	case <-stop:
	}
	application.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		return fmtError("shutdown", err)
	}
	if localServer != nil {
		if err := localServer.Shutdown(ctx); err != nil {
			return fmtError("shutdown local API", err)
		}
	}
	if serveErr != nil {
		return fmtError("serve Kanban", serveErr)
	}
	return nil
}

func listenLocalSocket(path string) (net.Listener, error) {
	info, err := os.Lstat(path)
	switch {
	case err == nil:
		if info.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("path exists and is not a socket")
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	case !errors.Is(err, os.ErrNotExist):
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		listener.Close()
		os.Remove(path)
		return nil, err
	}
	return listener, nil
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

func configuredLocalSocket(dataDir, actor string) string {
	if runtime.GOOS == "windows" {
		return ""
	}
	if path, configured := os.LookupEnv("KANBAN_LOCAL_SOCKET"); configured {
		return strings.TrimSpace(path)
	}
	if actor == "" {
		return ""
	}
	return filepath.Join(dataDir, "kanban.sock")
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
