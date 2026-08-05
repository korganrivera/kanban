package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
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
	WIPLimits(context.Context) (board.WIPLimits, error)
	UpdateWIPLimits(context.Context, board.WIPLimits) (board.WIPLimits, error)
	CreateRemedy(context.Context, string, string, board.RemedyInput) (*board.RemedyResult, error)
	RegistrationEnabled(context.Context, bool) (bool, error)
	RegisterUser(context.Context, string, string, bool, time.Time) (*store.User, error)
	User(context.Context, string) (*store.User, error)
	UpdatePassword(context.Context, string, string, time.Time) error
	CreateSession(context.Context, string, string, time.Time, time.Time) error
	SessionUser(context.Context, string, time.Time, time.Time) (*store.User, error)
	DeleteSession(context.Context, string) error
	CompletionHistory(context.Context, string) ([]store.CompletionEntry, error)
}

type Config struct {
	AuthEnabled       bool
	Actor             string
	AllowRegistration bool
	CookieSecure      bool
	SessionTTL        time.Duration
}

type Server struct {
	store     taskStore
	config    Config
	mux       *http.ServeMux
	events    *broker
	limiter   *loginLimiter
	dummyHash []byte
	now       func() time.Time
	closed    chan struct{}
	close     sync.Once
}

func New(store *store.Store, config Config) *Server {
	config.Actor = strings.TrimSpace(config.Actor)
	if config.Actor == "" {
		config.Actor = "local"
	}
	if config.SessionTTL <= 0 {
		config.SessionTTL = 7 * 24 * time.Hour
	}
	server := &Server{
		store:   store,
		config:  config,
		mux:     http.NewServeMux(),
		events:  newBroker(),
		limiter: newLoginLimiter(20, 15*time.Minute),
		now:     time.Now,
		closed:  make(chan struct{}),
	}
	server.initializeDummyHash()
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
	server.mux.HandleFunc("GET /api/auth/registration", server.registrationStatus)
	server.mux.HandleFunc("POST /api/auth/register", server.register)
	server.mux.HandleFunc("POST /api/auth/login", server.login)
	server.mux.HandleFunc("GET /api/auth/me", server.me)
	server.mux.Handle("POST /api/auth/change-password", server.requireAuth(http.HandlerFunc(server.changePassword)))
	server.mux.Handle("POST /api/auth/logout", server.requireAuth(http.HandlerFunc(server.logout)))
	server.mux.Handle("GET /api/account/completions", server.requireAuth(http.HandlerFunc(server.completionHistory)))
	server.mux.Handle("GET /api/tasks", server.requireAuth(http.HandlerFunc(server.listTasks)))
	server.mux.Handle("POST /api/tasks", server.requireAuth(http.HandlerFunc(server.createTask)))
	server.mux.Handle("PATCH /api/tasks/{id}", server.requireAuth(http.HandlerFunc(server.updateTask)))
	server.mux.Handle("DELETE /api/tasks/{id}", server.requireAuth(http.HandlerFunc(server.deleteTask)))
	server.mux.Handle("POST /api/tasks/{id}/remedy", server.requireAuth(http.HandlerFunc(server.createRemedy)))
	server.mux.Handle("POST /api/tasks/{id}/{action}", server.requireAuth(http.HandlerFunc(server.actionTask)))
	server.mux.Handle("GET /api/wip-limits", server.requireAuth(http.HandlerFunc(server.getWIPLimits)))
	server.mux.Handle("PATCH /api/wip-limits", server.requireAuth(http.HandlerFunc(server.updateWIPLimits)))
	server.mux.Handle("GET /api/events", server.requireAuth(http.HandlerFunc(server.streamEvents)))
	server.mux.Handle("GET /", webui.Handler())
}

func (server *Server) completionHistory(response http.ResponseWriter, request *http.Request) {
	entries, err := server.store.CompletionHistory(request.Context(), requestIdentity(request).Username)
	if err != nil {
		log.Printf("completion history: %v", err)
		writeError(response, http.StatusInternalServerError, "could not load completion history")
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"entries": entries})
}

func (server *Server) createRemedy(response http.ResponseWriter, request *http.Request) {
	var input board.RemedyInput
	if err := decodeJSON(response, request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	actor := ""
	if server.config.AuthEnabled {
		actor = requestIdentity(request).Username
	}
	result, err := server.store.CreateRemedy(request.Context(), request.PathValue("id"), actor, input)
	if err != nil {
		writeStoreError(response, err)
		return
	}
	server.events.publish()
	writeJSON(response, http.StatusCreated, result)
}

func (server *Server) getWIPLimits(response http.ResponseWriter, request *http.Request) {
	limits, err := server.store.WIPLimits(request.Context())
	if err != nil {
		log.Printf("get WIP limits: %v", err)
		writeError(response, http.StatusInternalServerError, "could not load WIP limits")
		return
	}
	writeJSON(response, http.StatusOK, limits)
}

func (server *Server) updateWIPLimits(response http.ResponseWriter, request *http.Request) {
	var input board.WIPLimits
	if err := decodeJSON(response, request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	limits, err := server.store.UpdateWIPLimits(request.Context(), input)
	if err != nil {
		writeStoreError(response, err)
		return
	}
	server.events.publish()
	writeJSON(response, http.StatusOK, limits)
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
	if server.config.AuthEnabled {
		actor := requestIdentity(request).Username
		input.CreatedBy = &actor
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
		request.Context(), request.PathValue("id"), request.PathValue("action"), requestIdentity(request).Username, input,
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
	identity := requestIdentity(request)
	updates, unsubscribe := server.events.subscribe(identity.Username, identity.SessionHash)
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
		case _, ok := <-updates:
			if !ok {
				return
			}
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
	var wipError *board.WIPLimitError
	switch {
	case errors.Is(err, board.ErrNotFound):
		writeError(response, http.StatusNotFound, err.Error())
	case errors.Is(err, board.ErrConflict):
		writeError(response, http.StatusConflict, err.Error())
	case errors.Is(err, board.ErrInvalidAction):
		writeError(response, http.StatusConflict, err.Error())
	case errors.As(err, &wipError):
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
		if request.Method != http.MethodGet && request.Method != http.MethodHead && request.Method != http.MethodOptions {
			if origin := request.Header.Get("Origin"); origin != "" {
				parsed, err := url.Parse(origin)
				if err != nil || !strings.EqualFold(parsed.Host, request.Host) {
					writeError(response, http.StatusForbidden, "cross-origin request rejected")
					return
				}
			}
			if site := request.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
				writeError(response, http.StatusForbidden, "cross-origin request rejected")
				return
			}
		}
		next.ServeHTTP(response, request)
	})
}
