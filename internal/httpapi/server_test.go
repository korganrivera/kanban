package httpapi

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestLegacyRouteEquivalentsThroughHTTP(t *testing.T) {
	handler := testServer(t)
	for _, endpoint := range []string{"/healthz", "/api/tasks", "/api/wip-limits"} {
		response := performJSON(t, handler, http.MethodGet, endpoint, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, body = %s", endpoint, response.Code, response.Body.String())
		}
	}

	created := performJSON(t, handler, http.MethodPost, "/api/tasks", map[string]string{"title": "Route coverage"})
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", created.Code, created.Body.String())
	}
	task := decodeTask(t, created)
	updated := performJSON(t, handler, http.MethodPatch, "/api/tasks/"+task.ID, board.TaskInput{
		Title: "Updated route coverage", Description: "Edited", Recurrence: task.Recurrence, Version: task.Version,
	})
	if updated.Code != http.StatusOK {
		t.Fatalf("update status = %d, body = %s", updated.Code, updated.Body.String())
	}
	task = decodeTask(t, updated)

	for _, transition := range []struct {
		action string
		state  board.EffectiveState
	}{
		{"block", board.StateBlocked},
		{"unblock", board.StateReady},
		{"claim", board.StateInProgress},
		{"release", board.StateReady},
		{"complete", board.StateDone},
		{"undo", board.StateReady},
	} {
		response := performJSON(t, handler, http.MethodPost, "/api/tasks/"+task.ID+"/"+transition.action, board.ActionInput{
			Version: task.Version, Note: "Route test blocker",
		})
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d, body = %s", transition.action, response.Code, response.Body.String())
		}
		task = decodeTask(t, response)
		if task.EffectiveState != transition.state {
			t.Fatalf("%s state = %s, want %s", transition.action, task.EffectiveState, transition.state)
		}
	}

	deleted := performJSON(t, handler, http.MethodDelete, "/api/tasks/"+task.ID, board.DeleteInput{Version: task.Version})
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %s", deleted.Code, deleted.Body.String())
	}
	listed := performJSON(t, handler, http.MethodGet, "/api/tasks", nil)
	if listed.Code != http.StatusOK || listed.Body.String() != "[]\n" {
		t.Fatalf("task list after delete = %d, %s", listed.Code, listed.Body.String())
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
	pointsTaskResponse := performJSONWithCookie(t, handler, http.MethodPost, "/api/tasks", map[string]string{
		"title": "Earn points",
	}, firstCookie)
	pointsTask := decodeTask(t, pointsTaskResponse)
	pointsTaskResponse = performJSONWithCookie(t, handler, http.MethodPost, "/api/tasks/"+pointsTask.ID+"/claim", board.ActionInput{
		Version: pointsTask.Version,
	}, firstCookie)
	pointsTask = decodeTask(t, pointsTaskResponse)
	pointsTaskResponse = performJSONWithCookie(t, handler, http.MethodPost, "/api/tasks/"+pointsTask.ID+"/complete", board.ActionInput{
		Version: pointsTask.Version,
	}, firstCookie)
	if pointsTaskResponse.Code != http.StatusOK {
		t.Fatalf("point completion status = %d, body = %s", pointsTaskResponse.Code, pointsTaskResponse.Body.String())
	}
	pointsTask = decodeTask(t, pointsTaskResponse)
	if pointsTask.Awarded == nil || pointsTask.PointsSnapshot == nil || pointsTask.Awarded.Points != *pointsTask.PointsSnapshot {
		t.Fatalf("point completion award = %#v, snapshot = %v", pointsTask.Awarded, pointsTask.PointsSnapshot)
	}
	accountResponse := performJSONWithCookie(t, handler, http.MethodGet, "/api/auth/me", nil, firstCookie)
	var account struct {
		Points int `json:"points"`
	}
	if err := json.NewDecoder(accountResponse.Body).Decode(&account); err != nil {
		t.Fatal(err)
	}
	if account.Points != pointsTask.Awarded.Points {
		t.Fatalf("account points after completion = %d, want %d", account.Points, pointsTask.Awarded.Points)
	}
	history := performJSONWithCookie(t, handler, http.MethodGet, "/api/account/completions", nil, firstCookie)
	if history.Code != http.StatusOK || !strings.Contains(history.Body.String(), `"taskTitle":"Earn points"`) {
		t.Fatalf("completion history = %d, %s", history.Code, history.Body.String())
	}
	pointsTaskResponse = performJSONWithCookie(t, handler, http.MethodPost, "/api/tasks/"+pointsTask.ID+"/undo", board.ActionInput{
		Version: pointsTask.Version,
	}, firstCookie)
	if pointsTaskResponse.Code != http.StatusOK {
		t.Fatalf("point undo status = %d, body = %s", pointsTaskResponse.Code, pointsTaskResponse.Body.String())
	}
	history = performJSONWithCookie(t, handler, http.MethodGet, "/api/account/completions", nil, firstCookie)
	if history.Code != http.StatusOK || strings.Contains(history.Body.String(), `"taskTitle":"Earn points"`) {
		t.Fatalf("completion history after undo = %d, %s", history.Code, history.Body.String())
	}
	accountResponse = performJSONWithCookie(t, handler, http.MethodGet, "/api/auth/me", nil, firstCookie)
	if err := json.NewDecoder(accountResponse.Body).Decode(&account); err != nil {
		t.Fatal(err)
	}
	if account.Points != 0 {
		t.Fatalf("account points after undo = %d", account.Points)
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

func TestMultiSessionLiveUpdatesStaleEditsAndRevocation(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "kanban.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	application := New(database, Config{AuthEnabled: true})
	webServer := httptest.NewServer(application.Handler())
	t.Cleanup(webServer.Close)
	t.Cleanup(application.Close)
	t.Cleanup(webServer.CloseClientConnections)

	first := newSessionClient(t)
	second := newSessionClient(t)
	register := clientJSON(t, first, http.MethodPost, webServer.URL+"/api/auth/register", map[string]string{
		"username": "alice", "password": "correct horse battery staple",
	})
	if register.StatusCode != http.StatusCreated {
		t.Fatalf("registration status = %d, body = %s", register.StatusCode, readBody(t, register))
	}
	register.Body.Close()
	login := clientJSON(t, second, http.MethodPost, webServer.URL+"/api/auth/login", map[string]string{
		"username": "alice", "password": "correct horse battery staple",
	})
	if login.StatusCode != http.StatusOK {
		t.Fatalf("second login status = %d, body = %s", login.StatusCode, readBody(t, login))
	}
	login.Body.Close()

	eventRequest, err := http.NewRequest(http.MethodGet, webServer.URL+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	events, err := second.Do(eventRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer events.Body.Close()
	if events.StatusCode != http.StatusOK || events.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("event stream response = %d, %s", events.StatusCode, events.Header.Get("Content-Type"))
	}
	eventReader := bufio.NewReader(events.Body)
	awaitBoardEvent(t, eventReader)

	createdResponse := clientJSON(t, first, http.MethodPost, webServer.URL+"/api/tasks", map[string]string{"title": "Shared edit"})
	if createdResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", createdResponse.StatusCode, readBody(t, createdResponse))
	}
	var task board.Task
	if err := json.NewDecoder(createdResponse.Body).Decode(&task); err != nil {
		t.Fatal(err)
	}
	createdResponse.Body.Close()
	awaitBoardEvent(t, eventReader)

	update := board.TaskInput{Title: "First edit", Recurrence: task.Recurrence, Version: task.Version}
	updated := clientJSON(t, first, http.MethodPatch, webServer.URL+"/api/tasks/"+task.ID, update)
	if updated.StatusCode != http.StatusOK {
		t.Fatalf("first update status = %d, body = %s", updated.StatusCode, readBody(t, updated))
	}
	updated.Body.Close()
	awaitBoardEvent(t, eventReader)
	stale := clientJSON(t, second, http.MethodPatch, webServer.URL+"/api/tasks/"+task.ID, board.TaskInput{
		Title: "Stale edit", Recurrence: task.Recurrence, Version: task.Version,
	})
	if stale.StatusCode != http.StatusConflict {
		t.Fatalf("stale update status = %d, body = %s", stale.StatusCode, readBody(t, stale))
	}
	stale.Body.Close()

	streamClosed := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, eventReader)
		streamClosed <- err
	}()
	changed := clientJSON(t, first, http.MethodPost, webServer.URL+"/api/auth/change-password", map[string]string{
		"currentPassword": "correct horse battery staple", "newPassword": "a different long password",
	})
	if changed.StatusCode != http.StatusOK {
		t.Fatalf("password change status = %d, body = %s", changed.StatusCode, readBody(t, changed))
	}
	changed.Body.Close()
	select {
	case <-streamClosed:
	case <-time.After(2 * time.Second):
		t.Fatal("revoked session event stream remained open")
	}
	revoked := clientJSON(t, second, http.MethodGet, webServer.URL+"/api/tasks", nil)
	if revoked.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked session status = %d, body = %s", revoked.StatusCode, readBody(t, revoked))
	}
	revoked.Body.Close()
}

func newSessionClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar, Timeout: 5 * time.Second}
}

func clientJSON(t *testing.T, client *http.Client, method, endpoint string, input any) *http.Response {
	t.Helper()
	var body io.Reader
	if input != nil {
		var encoded bytes.Buffer
		if err := json.NewEncoder(&encoded).Encode(input); err != nil {
			t.Fatal(err)
		}
		body = &encoded
	}
	request, err := http.NewRequest(method, endpoint, body)
	if err != nil {
		t.Fatal(err)
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func awaitBoardEvent(t *testing.T, reader *bufio.Reader) {
	t.Helper()
	result := make(chan error, 1)
	go func() {
		seenRefresh := false
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				result <- err
				return
			}
			line = strings.TrimSpace(line)
			if line == "data: refresh" {
				seenRefresh = true
			}
			if line == "" && seenRefresh {
				result <- nil
				return
			}
		}
	}()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("read board event: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for board event")
	}
}

func readBody(t *testing.T, response *http.Response) string {
	t.Helper()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
