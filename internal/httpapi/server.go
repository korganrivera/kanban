package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"kanban-go/internal/board"
	"kanban-go/internal/store"
	"kanban-go/internal/webui"
)

type taskStore interface {
	List(context.Context) ([]*board.Task, error)
	Get(context.Context, string) (*board.Task, error)
	Create(context.Context, board.TaskInput) (*board.Task, error)
	Update(context.Context, string, board.TaskInput) (*board.Task, error)
	Action(context.Context, string, string, string, board.ActionInput) (*board.Task, error)
	Delete(context.Context, string, board.DeleteInput) ([]board.TaskReference, error)
}

type Server struct {
	store  taskStore
	actor  string
	mux    *http.ServeMux
	events *broker
	closed chan struct{}
	close  sync.Once
}

func New(store *store.Store, actor string) *Server {
	server := &Server{
		store:  store,
		actor:  strings.TrimSpace(actor),
		mux:    http.NewServeMux(),
		events: newBroker(),
		closed: make(chan struct{}),
	}
	server.routes()
	return server
}

func (server *Server) Handler() http.Handler {
	return securityHeaders(server.mux)
}

func (server *Server) Close() {
	server.close.Do(func() { close(server.closed) })
}

func (server *Server) routes() {
	server.mux.HandleFunc("GET /healthz", server.health)
	server.mux.HandleFunc("GET /api/tasks", server.listTasks)
	server.mux.HandleFunc("POST /api/tasks", server.createTask)
	server.mux.HandleFunc("PATCH /api/tasks/{id}", server.updateTask)
	server.mux.HandleFunc("DELETE /api/tasks/{id}", server.deleteTask)
	server.mux.HandleFunc("POST /api/tasks/{id}/{action}", server.actionTask)
	server.mux.HandleFunc("GET /api/events", server.streamEvents)
	server.mux.Handle("GET /", webui.Handler())
}

func (server *Server) deleteTask(response http.ResponseWriter, request *http.Request) {
	var input board.DeleteInput
	if err := decodeJSON(response, request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	dependents, err := server.store.Delete(request.Context(), request.PathValue("id"), input)
	if errors.Is(err, board.ErrHasDependents) {
		writeJSON(response, http.StatusConflict, map[string]any{
			"error":      err.Error(),
			"dependents": dependents,
		})
		return
	}
	if err != nil {
		writeStoreError(response, err)
		return
	}
	server.events.publish()
	writeJSON(response, http.StatusOK, map[string]any{
		"deleted":            request.PathValue("id"),
		"adjustedDependents": dependents,
	})
}

func (server *Server) health(response http.ResponseWriter, request *http.Request) {
	if _, err := server.store.List(request.Context()); err != nil {
		writeError(response, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	writeJSON(response, http.StatusOK, map[string]string{"status": "ok"})
}

func (server *Server) listTasks(response http.ResponseWriter, request *http.Request) {
	tasks, err := server.store.List(request.Context())
	if err != nil {
		log.Printf("list tasks: %v", err)
		writeError(response, http.StatusInternalServerError, "could not load tasks")
		return
	}
	writeJSON(response, http.StatusOK, tasks)
}

func (server *Server) createTask(response http.ResponseWriter, request *http.Request) {
	var input board.TaskInput
	if err := decodeJSON(response, request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	task, err := server.store.Create(request.Context(), input)
	if err != nil {
		writeStoreError(response, err)
		return
	}
	server.events.publish()
	writeJSON(response, http.StatusCreated, task)
}

func (server *Server) updateTask(response http.ResponseWriter, request *http.Request) {
	var input board.TaskInput
	if err := decodeJSON(response, request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	task, err := server.store.Update(request.Context(), request.PathValue("id"), input)
	if err != nil {
		writeStoreError(response, err)
		return
	}
	server.events.publish()
	writeJSON(response, http.StatusOK, task)
}

func (server *Server) actionTask(response http.ResponseWriter, request *http.Request) {
	var input board.ActionInput
	if err := decodeJSON(response, request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	task, err := server.store.Action(
		request.Context(), request.PathValue("id"), request.PathValue("action"), server.actor, input,
	)
	if err != nil {
		writeStoreError(response, err)
		return
	}
	server.events.publish()
	writeJSON(response, http.StatusOK, task)
}

func (server *Server) streamEvents(response http.ResponseWriter, request *http.Request) {
	flusher, ok := response.(http.Flusher)
	if !ok {
		writeError(response, http.StatusInternalServerError, "streaming unavailable")
		return
	}
	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("Cache-Control", "no-cache")
	response.Header().Set("Connection", "keep-alive")
	updates, unsubscribe := server.events.subscribe()
	defer unsubscribe()
	fmt.Fprint(response, "event: board\ndata: refresh\n\n")
	flusher.Flush()
	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-request.Context().Done():
			return
		case <-server.closed:
			return
		case <-updates:
			fmt.Fprint(response, "event: board\ndata: refresh\n\n")
			flusher.Flush()
		case <-heartbeat.C:
			fmt.Fprint(response, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

func decodeJSON(response http.ResponseWriter, request *http.Request, target any) error {
	request.Body = http.MaxBytesReader(response, request.Body, 1<<20)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid request: %w", err)
	}
	if decoder.Decode(&struct{}{}) == nil {
		return errors.New("invalid request: multiple JSON values")
	}
	return nil
}

func writeStoreError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, board.ErrNotFound):
		writeError(response, http.StatusNotFound, err.Error())
	case errors.Is(err, board.ErrConflict):
		writeError(response, http.StatusConflict, err.Error())
	case errors.Is(err, board.ErrInvalidAction):
		writeError(response, http.StatusConflict, err.Error())
	default:
		writeError(response, http.StatusBadRequest, err.Error())
	}
}

func writeError(response http.ResponseWriter, status int, message string) {
	writeJSON(response, status, map[string]string{"error": message})
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	if err := json.NewEncoder(response).Encode(value); err != nil {
		log.Printf("write response: %v", err)
	}
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("X-Content-Type-Options", "nosniff")
		response.Header().Set("X-Frame-Options", "DENY")
		response.Header().Set("Referrer-Policy", "no-referrer")
		response.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'")
		next.ServeHTTP(response, request)
	})
}
