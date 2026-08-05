package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"kanban-go/internal/store"
)

const sessionCookieName = "kanban_session"

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,32}$`)

type identity struct {
	Username    string
	SessionHash string
}

type identityContextKey struct{}

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type passwordChange struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

type loginLimiter struct {
	mu       sync.Mutex
	limit    int
	window   time.Duration
	attempts map[string][]time.Time
}

func newLoginLimiter(limit int, window time.Duration) *loginLimiter {
	return &loginLimiter{limit: limit, window: window, attempts: make(map[string][]time.Time)}
}

func (limiter *loginLimiter) allow(key string, now time.Time) bool {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	cutoff := now.Add(-limiter.window)
	recent := limiter.attempts[key][:0]
	for _, attempt := range limiter.attempts[key] {
		if attempt.After(cutoff) {
			recent = append(recent, attempt)
		}
	}
	if len(recent) >= limiter.limit {
		limiter.attempts[key] = recent
		return false
	}
	limiter.attempts[key] = append(recent, now)
	return true
}

func (limiter *loginLimiter) reset(key string) {
	limiter.mu.Lock()
	delete(limiter.attempts, key)
	limiter.mu.Unlock()
}

func (server *Server) initializeDummyHash() {
	hash, err := bcrypt.GenerateFromPassword([]byte("invalid-login-placeholder"), bcrypt.DefaultCost)
	if err != nil {
		panic(err)
	}
	server.dummyHash = hash
}

func (server *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if !server.config.AuthEnabled {
			identity := identity{Username: server.config.Actor}
			next.ServeHTTP(response, request.WithContext(context.WithValue(request.Context(), identityContextKey{}, identity)))
			return
		}
		user, identity, ok := server.authenticate(response, request)
		if !ok {
			writeError(response, http.StatusUnauthorized, "authentication required")
			return
		}
		identity.Username = user.Username
		next.ServeHTTP(response, request.WithContext(context.WithValue(request.Context(), identityContextKey{}, identity)))
	})
}

func (server *Server) authenticate(response http.ResponseWriter, request *http.Request) (*store.User, identity, bool) {
	cookie, err := request.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return nil, identity{}, false
	}
	now := server.now().UTC()
	hash := hashToken(cookie.Value)
	user, err := server.store.SessionUser(request.Context(), hash, now, now.Add(server.config.SessionTTL))
	if err != nil {
		if !errors.Is(err, store.ErrSessionNotFound) {
			log.Printf("authenticate session: %v", err)
		}
		server.clearSessionCookie(response)
		return nil, identity{}, false
	}
	server.setSessionCookie(response, cookie.Value, now.Add(server.config.SessionTTL))
	return user, identity{Username: user.Username, SessionHash: hash}, true
}

func requestIdentity(request *http.Request) identity {
	value, _ := request.Context().Value(identityContextKey{}).(identity)
	return value
}

func (server *Server) registrationStatus(response http.ResponseWriter, request *http.Request) {
	enabled, err := server.store.RegistrationEnabled(request.Context(), server.config.AllowRegistration)
	if err != nil {
		log.Printf("registration status: %v", err)
		writeError(response, http.StatusInternalServerError, "could not check registration status")
		return
	}
	writeJSON(response, http.StatusOK, map[string]bool{"enabled": enabled})
}

func (server *Server) register(response http.ResponseWriter, request *http.Request) {
	var input credentials
	if err := decodeJSON(response, request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	input.Username = strings.TrimSpace(input.Username)
	if !usernamePattern.MatchString(input.Username) {
		writeError(response, http.StatusBadRequest, "username must be 3-32 characters using letters, numbers, period, underscore, or hyphen")
		return
	}
	if err := validatePassword(input.Password); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "could not create account")
		return
	}
	now := server.now().UTC()
	user, err := server.store.RegisterUser(request.Context(), input.Username, string(hash), server.config.AllowRegistration, now)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrRegistrationDisabled):
			writeError(response, http.StatusForbidden, err.Error())
		case errors.Is(err, store.ErrUserExists):
			writeError(response, http.StatusConflict, err.Error())
		default:
			log.Printf("register user: %v", err)
			writeError(response, http.StatusInternalServerError, "could not create account")
		}
		return
	}
	if _, err := server.issueSession(response, request, user.Username, now); err != nil {
		log.Printf("create registration session: %v", err)
		writeError(response, http.StatusInternalServerError, "account created, but login failed")
		return
	}
	writeJSON(response, http.StatusCreated, publicUser(user))
}

func (server *Server) login(response http.ResponseWriter, request *http.Request) {
	now := server.now().UTC()
	key := clientAddress(request)
	if !server.limiter.allow(key, now) {
		writeError(response, http.StatusTooManyRequests, "too many login attempts; try again later")
		return
	}
	var input credentials
	if err := decodeJSON(response, request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	input.Username = strings.TrimSpace(input.Username)
	if len(input.Username) > 64 || len(input.Password) > 200 {
		writeError(response, http.StatusUnauthorized, "invalid username or password")
		return
	}
	user, err := server.store.User(request.Context(), input.Username)
	hash := server.dummyHash
	if err == nil && user.PasswordHash != "" {
		hash = []byte(user.PasswordHash)
	}
	passwordErr := bcrypt.CompareHashAndPassword(hash, []byte(input.Password))
	if err != nil || passwordErr != nil || user.PasswordHash == "" {
		if err != nil && !errors.Is(err, store.ErrUserNotFound) {
			log.Printf("login lookup: %v", err)
		}
		writeError(response, http.StatusUnauthorized, "invalid username or password")
		return
	}
	if _, err := server.issueSession(response, request, user.Username, now); err != nil {
		log.Printf("create login session: %v", err)
		writeError(response, http.StatusInternalServerError, "could not log in")
		return
	}
	server.limiter.reset(key)
	writeJSON(response, http.StatusOK, publicUser(user))
}

func (server *Server) me(response http.ResponseWriter, request *http.Request) {
	if !server.config.AuthEnabled {
		writeJSON(response, http.StatusOK, map[string]any{
			"authenticated": true,
			"username":      server.config.Actor,
		})
		return
	}
	user, _, ok := server.authenticate(response, request)
	if !ok {
		writeJSON(response, http.StatusOK, map[string]bool{"authenticated": false})
		return
	}
	result := publicUser(user)
	result["authenticated"] = true
	writeJSON(response, http.StatusOK, result)
}

func (server *Server) changePassword(response http.ResponseWriter, request *http.Request) {
	var input passwordChange
	if err := decodeJSON(response, request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	if err := validatePassword(input.NewPassword); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	identity := requestIdentity(request)
	user, err := server.store.User(request.Context(), identity.Username)
	if err != nil {
		writeError(response, http.StatusUnauthorized, "authentication required")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(input.CurrentPassword)) != nil {
		writeError(response, http.StatusUnauthorized, "current password is incorrect")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "could not change password")
		return
	}
	now := server.now().UTC()
	if err := server.store.UpdatePassword(request.Context(), identity.Username, string(hash), now); err != nil {
		log.Printf("change password: %v", err)
		writeError(response, http.StatusInternalServerError, "could not change password")
		return
	}
	server.events.disconnectUser(identity.Username)
	if _, err := server.issueSession(response, request, identity.Username, now); err != nil {
		log.Printf("create replacement session: %v", err)
		server.clearSessionCookie(response)
		writeError(response, http.StatusInternalServerError, "password changed; please log in again")
		return
	}
	writeJSON(response, http.StatusOK, map[string]string{"status": "password changed"})
}

func (server *Server) logout(response http.ResponseWriter, request *http.Request) {
	identity := requestIdentity(request)
	if identity.SessionHash != "" {
		if err := server.store.DeleteSession(request.Context(), identity.SessionHash); err != nil {
			log.Printf("delete session: %v", err)
			writeError(response, http.StatusInternalServerError, "could not log out")
			return
		}
		server.events.disconnectSession(identity.SessionHash)
	}
	server.clearSessionCookie(response)
	writeJSON(response, http.StatusOK, map[string]string{"status": "logged out"})
}

func (server *Server) issueSession(response http.ResponseWriter, request *http.Request, username string, now time.Time) (identity, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return identity{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	tokenHash := hashToken(token)
	expiresAt := now.Add(server.config.SessionTTL)
	if err := server.store.CreateSession(request.Context(), tokenHash, username, now, expiresAt); err != nil {
		return identity{}, err
	}
	server.setSessionCookie(response, token, expiresAt)
	return identity{Username: username, SessionHash: tokenHash}, nil
}

func (server *Server) setSessionCookie(response http.ResponseWriter, token string, expiresAt time.Time) {
	http.SetCookie(response, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		MaxAge:   int(server.config.SessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   server.config.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (server *Server) clearSessionCookie(response http.ResponseWriter) {
	http.SetCookie(response, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(1, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   server.config.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

func hashToken(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

func publicUser(user *store.User) map[string]any {
	return map[string]any{"username": user.Username}
}

func validatePassword(password string) error {
	if len(password) < 10 || len(password) > 200 {
		return errors.New("password must be 10-200 characters")
	}
	return nil
}

func clientAddress(request *http.Request) string {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err == nil {
		return host
	}
	return request.RemoteAddr
}
