package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"kanban-go/internal/httpapi"
	"kanban-go/internal/store"
)

func main() {
	syscall.Umask(0o077)
	address := envOr("KANBAN_ADDR", "127.0.0.1:3100")
	dataDir := envOr("KANBAN_DATA_DIR", "data")

	database, err := store.Open(filepath.Join(dataDir, "kanban.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer database.Close()

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

	go func() {
		log.Printf("Kanban Go listening on http://%s", address)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	application.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("shutdown: %v", err)
	}
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
