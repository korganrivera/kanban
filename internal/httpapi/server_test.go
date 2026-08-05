package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"kanban-go/internal/board"
	"kanban-go/internal/store"
)

func testServer(t *testing.T) http.Handler {
	t.Helper()
	database, err := store.Open(filepath.Join(t.TempDir(), "kanban.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return New(database, "tester").Handler()
}

func performJSON(t *testing.T, handler http.Handler, method, path string, input any) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	if input != nil {
		if err := json.NewEncoder(&body).Encode(input); err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, &body)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodeTask(t *testing.T, response *httptest.ResponseRecorder) board.Task {
	t.Helper()
	var task board.Task
	if err := json.NewDecoder(response.Body).Decode(&task); err != nil {
		t.Fatal(err)
	}
	return task
}

func TestTaskWorkflowThroughHTTP(t *testing.T) {
	handler := testServer(t)
	created := performJSON(t, handler, http.MethodPost, "/api/tasks", map[string]string{
		"title": "Test the browser API",
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", created.Code, created.Body.String())
	}
	task := decodeTask(t, created)

	claimed := performJSON(t, handler, http.MethodPost, "/api/tasks/"+task.ID+"/claim", board.ActionInput{Version: task.Version})
	if claimed.Code != http.StatusOK {
		t.Fatalf("claim status = %d, body = %s", claimed.Code, claimed.Body.String())
	}
	task = decodeTask(t, claimed)
	if task.EffectiveState != board.StateInProgress || task.ClaimedBy == nil || *task.ClaimedBy != "tester" {
		t.Fatalf("claimed task = state %s, owner %v", task.EffectiveState, task.ClaimedBy)
	}

	completed := performJSON(t, handler, http.MethodPost, "/api/tasks/"+task.ID+"/complete", board.ActionInput{Version: task.Version})
	if completed.Code != http.StatusOK {
		t.Fatalf("complete status = %d, body = %s", completed.Code, completed.Body.String())
	}
	task = decodeTask(t, completed)
	if task.EffectiveState != board.StateDone || !task.CanUndo {
		t.Fatalf("completed task = state %s, canUndo %v", task.EffectiveState, task.CanUndo)
	}

	undone := performJSON(t, handler, http.MethodPost, "/api/tasks/"+task.ID+"/undo", board.ActionInput{Version: task.Version})
	if undone.Code != http.StatusOK {
		t.Fatalf("undo status = %d, body = %s", undone.Code, undone.Body.String())
	}
	if task = decodeTask(t, undone); task.EffectiveState != board.StateInProgress {
		t.Fatalf("undone task state = %s", task.EffectiveState)
	}
}

func TestStaticAssetsAndRequestValidation(t *testing.T) {
	handler := testServer(t)

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Kanban Go") {
		t.Fatalf("index response = %d, body = %q", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("content security policy header is missing")
	}

	invalid := performJSON(t, handler, http.MethodPost, "/api/tasks", map[string]any{
		"title": "Unknown field", "unexpected": true,
	})
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid request status = %d", invalid.Code)
	}
}
