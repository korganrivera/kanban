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
	return New(database, Config{Actor: "tester"}).Handler()
}

func performJSON(t *testing.T, handler http.Handler, method, path string, input any) *httptest.ResponseRecorder {
	return performJSONWithCookie(t, handler, method, path, input, nil)
}

func performJSONWithCookie(t *testing.T, handler http.Handler, method, path string, input any, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	if input != nil {
		if err := json.NewEncoder(&body).Encode(input); err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, &body)
	request.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func responseCookie(t *testing.T, response *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	var sessionCookie *http.Cookie
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == sessionCookieName {
			sessionCookie = cookie
		}
	}
	if sessionCookie != nil {
		return sessionCookie
	}
	t.Fatal("response did not include a session cookie")
	return nil
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

func TestWIPLimitsAndRemediesThroughHTTP(t *testing.T) {
	handler := testServer(t)
	limit := 1
	limits := performJSON(t, handler, http.MethodPatch, "/api/wip-limits", board.WIPLimits{
		board.StateInProgress: &limit,
	})
	if limits.Code != http.StatusOK {
		t.Fatalf("limit update status = %d, body = %s", limits.Code, limits.Body.String())
	}

	firstResponse := performJSON(t, handler, http.MethodPost, "/api/tasks", map[string]string{"title": "First"})
	first := decodeTask(t, firstResponse)
	secondResponse := performJSON(t, handler, http.MethodPost, "/api/tasks", map[string]string{"title": "Second"})
	second := decodeTask(t, secondResponse)
	claimed := performJSON(t, handler, http.MethodPost, "/api/tasks/"+first.ID+"/claim", board.ActionInput{Version: first.Version})
	if claimed.Code != http.StatusOK {
		t.Fatalf("first claim status = %d", claimed.Code)
	}
	rejected := performJSON(t, handler, http.MethodPost, "/api/tasks/"+second.ID+"/claim", board.ActionInput{Version: second.Version})
	if rejected.Code != http.StatusConflict || !strings.Contains(rejected.Body.String(), "WIP limit exceeded") {
		t.Fatalf("second claim response = %d, %s", rejected.Code, rejected.Body.String())
	}

	blockedResponse := performJSON(t, handler, http.MethodPost, "/api/tasks", map[string]string{"title": "Blocked"})
	blocked := decodeTask(t, blockedResponse)
	blockedResponse = performJSON(t, handler, http.MethodPost, "/api/tasks/"+blocked.ID+"/block", board.ActionInput{
		Version: blocked.Version, Note: "Needs a remedy",
	})
	blocked = decodeTask(t, blockedResponse)
	remedy := performJSON(t, handler, http.MethodPost, "/api/tasks/"+blocked.ID+"/remedy", board.RemedyInput{
		Title: "Fix it", Version: blocked.Version,
	})
	if remedy.Code != http.StatusCreated {
		t.Fatalf("remedy response = %d, %s", remedy.Code, remedy.Body.String())
	}
	var result board.RemedyResult
	if err := json.NewDecoder(remedy.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.BlockedTask.EffectiveState != board.StateSuspended || result.RemedyTask.EffectiveState != board.StateReady {
		t.Fatalf("remedy states = %s/%s", result.BlockedTask.EffectiveState, result.RemedyTask.EffectiveState)
	}
}

func TestAuthenticationAndPasswordRotation(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "kanban.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	server := New(database, Config{AuthEnabled: true})
	t.Cleanup(server.Close)
	handler := server.Handler()

	unauthorized := performJSON(t, handler, http.MethodGet, "/api/tasks", nil)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated task list status = %d", unauthorized.Code)
	}
	registration := performJSON(t, handler, http.MethodGet, "/api/auth/registration", nil)
	if registration.Code != http.StatusOK || !strings.Contains(registration.Body.String(), `"enabled":true`) {
		t.Fatalf("registration status = %d, %s", registration.Code, registration.Body.String())
	}

	registered := performJSON(t, handler, http.MethodPost, "/api/auth/register", map[string]string{
		"username": "alice", "password": "correct horse battery staple",
	})
	if registered.Code != http.StatusCreated {
		t.Fatalf("register status = %d, body = %s", registered.Code, registered.Body.String())
	}
	firstCookie := responseCookie(t, registered)
	if !firstCookie.HttpOnly || firstCookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie flags = HttpOnly %v, SameSite %v", firstCookie.HttpOnly, firstCookie.SameSite)
	}
	secondRegistration := performJSON(t, handler, http.MethodPost, "/api/auth/register", map[string]string{
		"username": "bob", "password": "another sufficiently long password",
	})
	if secondRegistration.Code != http.StatusForbidden {
		t.Fatalf("second registration status = %d, body = %s", secondRegistration.Code, secondRegistration.Body.String())
	}

	created := performJSONWithCookie(t, handler, http.MethodPost, "/api/tasks", map[string]string{
		"title": "Authenticated task",
	}, firstCookie)
	if created.Code != http.StatusCreated {
		t.Fatalf("authenticated create status = %d, body = %s", created.Code, created.Body.String())
	}
	task := decodeTask(t, created)
	if task.CreatedBy == nil || *task.CreatedBy != "alice" {
		t.Fatalf("task creator = %v", task.CreatedBy)
	}
	claimed := performJSONWithCookie(t, handler, http.MethodPost, "/api/tasks/"+task.ID+"/claim", board.ActionInput{Version: task.Version}, firstCookie)
	if claimed.Code != http.StatusOK {
		t.Fatalf("authenticated claim status = %d, body = %s", claimed.Code, claimed.Body.String())
	}
	task = decodeTask(t, claimed)
	if task.ClaimedBy == nil || *task.ClaimedBy != "alice" {
		t.Fatalf("task claimant = %v", task.ClaimedBy)
	}
	blocked := performJSONWithCookie(t, handler, http.MethodPost, "/api/tasks/"+task.ID+"/block", board.ActionInput{
		Version: task.Version, Note: "Needs a remedy",
	}, firstCookie)
	if blocked.Code != http.StatusOK {
		t.Fatalf("authenticated block status = %d, body = %s", blocked.Code, blocked.Body.String())
	}
	task = decodeTask(t, blocked)
	remedy := performJSONWithCookie(t, handler, http.MethodPost, "/api/tasks/"+task.ID+"/remedy", board.RemedyInput{
		Title: "Resolve authenticated blocker", Version: task.Version,
	}, firstCookie)
	if remedy.Code != http.StatusCreated {
		t.Fatalf("authenticated remedy status = %d, body = %s", remedy.Code, remedy.Body.String())
	}
	var remedyResult board.RemedyResult
	if err := json.NewDecoder(remedy.Body).Decode(&remedyResult); err != nil {
		t.Fatal(err)
	}
	if remedyResult.RemedyTask.CreatedBy == nil || *remedyResult.RemedyTask.CreatedBy != "alice" {
		t.Fatalf("remedy creator = %v", remedyResult.RemedyTask.CreatedBy)
	}

	secondLogin := performJSON(t, handler, http.MethodPost, "/api/auth/login", map[string]string{
		"username": "alice", "password": "correct horse battery staple",
	})
	if secondLogin.Code != http.StatusOK {
		t.Fatalf("second login status = %d, body = %s", secondLogin.Code, secondLogin.Body.String())
	}
	secondCookie := responseCookie(t, secondLogin)

	wrongPassword := performJSONWithCookie(t, handler, http.MethodPost, "/api/auth/change-password", map[string]string{
		"currentPassword": "incorrect password", "newPassword": "a different long password",
	}, firstCookie)
	if wrongPassword.Code != http.StatusUnauthorized {
		t.Fatalf("wrong current password status = %d", wrongPassword.Code)
	}
	changed := performJSONWithCookie(t, handler, http.MethodPost, "/api/auth/change-password", map[string]string{
		"currentPassword": "correct horse battery staple", "newPassword": "a different long password",
	}, firstCookie)
	if changed.Code != http.StatusOK {
		t.Fatalf("password change status = %d, body = %s", changed.Code, changed.Body.String())
	}
	replacementCookie := responseCookie(t, changed)

	revoked := performJSONWithCookie(t, handler, http.MethodGet, "/api/tasks", nil, secondCookie)
	if revoked.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session status = %d", revoked.Code)
	}
	current := performJSONWithCookie(t, handler, http.MethodGet, "/api/tasks", nil, replacementCookie)
	if current.Code != http.StatusOK {
		t.Fatalf("replacement session status = %d", current.Code)
	}
	oldLogin := performJSON(t, handler, http.MethodPost, "/api/auth/login", map[string]string{
		"username": "alice", "password": "correct horse battery staple",
	})
	if oldLogin.Code != http.StatusUnauthorized {
		t.Fatalf("old password login status = %d", oldLogin.Code)
	}

	logout := performJSONWithCookie(t, handler, http.MethodPost, "/api/auth/logout", map[string]any{}, replacementCookie)
	if logout.Code != http.StatusOK {
		t.Fatalf("logout status = %d, body = %s", logout.Code, logout.Body.String())
	}
	afterLogout := performJSONWithCookie(t, handler, http.MethodGet, "/api/tasks", nil, replacementCookie)
	if afterLogout.Code != http.StatusUnauthorized {
		t.Fatalf("logged-out session status = %d", afterLogout.Code)
	}
}

func TestCrossOriginMutationIsRejected(t *testing.T) {
	handler := testServer(t)
	request := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(`{"title":"Rejected"}`))
	request.Host = "kanban.example"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://attacker.example")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin response status = %d, body = %s", response.Code, response.Body.String())
	}
}
