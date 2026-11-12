package mcpgrafana

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	grafanaSessionFileEnvVar = "GRAFANA_SESSION_FILE"
	cursorCloudEnvVar        = "CURSOR_CLOUD"

	sessionCookieName       = "grafana_session"
	sessionExpiryCookieName = "grafana_session_expiry"
	sessionRotatePath       = "/api/user/auth-tokens/rotate"
)

// ErrSessionExpired is returned when the stored Grafana browser session can no
// longer be rotated and the user has to sign in again.
var ErrSessionExpired = errors.New("grafana browser session expired or was revoked; run `mcp-grafana login` to sign in again")

// SessionFile is the on-disk representation of a Grafana browser session
// captured by `mcp-grafana login`.
type SessionFile struct {
	URL       string            `json:"url"`
	Cookies   map[string]string `json:"cookies"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// SessionStore holds a Grafana browser session (the cookies set after an
// interactive SSO login) and keeps it alive by rotating the session token
// whenever Grafana asks for it. The session is persisted to a 0600 file so
// rotations survive restarts and are shared between several mcp-grafana
// processes using the same file.
type SessionStore struct {
	// path is the session file. It is empty for a value-backed store
	// (CURSOR_CLOUD), where the session lives only in memory and rotations
	// are handed to persist instead.
	path    string
	persist func(SessionFile) error

	mu      sync.Mutex
	session SessionFile
	modTime time.Time
}

// SessionFilePathFromEnv returns the value of GRAFANA_SESSION_FILE, or "" when
// browser-session auth is not enabled. It is a file path, unless
// CursorCloudEnabled reports true, in which case it is the session JSON itself.
func SessionFilePathFromEnv() string {
	return getEnv(grafanaSessionFileEnvVar)
}

// CursorCloudEnabled reports whether the server runs as a Cursor cloud agent
// (CURSOR_CLOUD=true). In that mode GRAFANA_SESSION_FILE holds the session
// contents rather than a path, and rotated sessions are written back to the
// user's personal Cursor secret (see PersistSessionToCursorSecret).
func CursorCloudEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(cursorCloudEnvVar)), "true")
}

// LoadSessionStoreFromValue builds a store from session JSON held in memory,
// as used when CURSOR_CLOUD=true. Every rotated session is passed to persist,
// since there is no file to write it to.
func LoadSessionStoreFromValue(value string, persist func(SessionFile) error) (*SessionStore, error) {
	if persist == nil {
		return nil, errors.New("a persist function is required for an in-memory session")
	}
	var sf SessionFile
	if err := json.Unmarshal([]byte(value), &sf); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", grafanaSessionFileEnvVar, err)
	}
	if sf.Cookies[sessionCookieName] == "" {
		return nil, fmt.Errorf("%s has no %s cookie; run `mcp-grafana login` and use the contents of the session file", grafanaSessionFileEnvVar, sessionCookieName)
	}
	return &SessionStore{session: sf, persist: persist}, nil
}

// DefaultSessionFilePath is where `mcp-grafana login` stores the session when
// no explicit path is given.
func DefaultSessionFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "mcp-grafana-session.json"
	}
	return filepath.Join(home, ".config", "mcp-grafana", "session.json")
}

// LoadSessionStore reads the session file at path.
func LoadSessionStore(path string) (*SessionStore, error) {
	s := &SessionStore{path: path}
	if err := s.reload(); err != nil {
		return nil, err
	}
	if s.session.Cookies[sessionCookieName] == "" {
		return nil, fmt.Errorf("session file %s has no %s cookie; run `mcp-grafana login`", path, sessionCookieName)
	}
	return s, nil
}

// URL returns the Grafana URL the session belongs to.
func (s *SessionStore) URL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.session.URL
}

// reload re-reads the session file. Callers must hold s.mu (or own s exclusively).
func (s *SessionStore) reload() error {
	if s.path == "" {
		// Value-backed store: the session is already in memory.
		return nil
	}
	// Stat before reading: if another process replaces the file in between we
	// pair the old mtime with the new contents, which only causes a harmless
	// extra reload, never a missed rotation.
	var modTime time.Time
	if fi, err := os.Stat(s.path); err == nil {
		modTime = fi.ModTime()
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("reading session file: %w", err)
	}
	var sf SessionFile
	if err := json.Unmarshal(data, &sf); err != nil {
		return fmt.Errorf("parsing session file %s: %w", s.path, err)
	}
	s.modTime = modTime
	s.session = sf
	return nil
}

// reloadIfChanged picks up rotations written by another process. Callers must hold s.mu.
func (s *SessionStore) reloadIfChanged() {
	fi, err := os.Stat(s.path)
	if err != nil || fi.ModTime().Equal(s.modTime) {
		return
	}
	_ = s.reload()
}

// snapshot returns the cookie header to send and the session token it carries.
func (s *SessionStore) snapshot() (cookieHeader, token string, needsRotation bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reloadIfChanged()
	token = s.session.Cookies[sessionCookieName]
	if exp, err := strconv.ParseInt(s.session.Cookies[sessionExpiryCookieName], 10, 64); err == nil {
		needsRotation = time.Now().Unix() >= exp
	}
	return CookieHeader(s.session.Cookies), token, needsRotation
}

// rotate exchanges the session token for a fresh one via Grafana's rotate
// endpoint. usedToken is the token the caller saw rejected (or about to
// expire); if the stored token already differs, someone else rotated it and
// nothing needs to be done.
func (s *SessionStore) rotate(ctx context.Context, rt http.RoundTripper, usedToken string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.path != "" {
		unlock, err := lockFile(s.path + ".lock")
		if err != nil {
			return err
		}
		defer unlock()
	}

	// Another process may have rotated while we waited for the lock.
	if err := s.reload(); err != nil {
		return err
	}
	if s.session.Cookies[sessionCookieName] != usedToken {
		return nil
	}

	rotateURL := strings.TrimRight(s.session.URL, "/") + sessionRotatePath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rotateURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Cookie", CookieHeader(s.session.Cookies))
	resp, err := rt.RoundTrip(req)
	if err != nil {
		return fmt.Errorf("rotating grafana session: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return ErrSessionExpired
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("rotating grafana session: unexpected status %s", resp.Status)
	}

	updated := false
	for _, c := range resp.Cookies() {
		if c.MaxAge < 0 || c.Value == "" {
			delete(s.session.Cookies, c.Name)
			continue
		}
		s.session.Cookies[c.Name] = c.Value
		updated = updated || c.Name == sessionCookieName
	}
	if !updated {
		return fmt.Errorf("rotating grafana session: response did not set a new %s cookie", sessionCookieName)
	}
	s.session.UpdatedAt = time.Now().UTC()
	if s.persist != nil {
		if err := s.persist(s.session); err != nil {
			return fmt.Errorf("saving rotated grafana session: %w", err)
		}
		return nil
	}
	if err := WriteSessionFile(s.path, s.session); err != nil {
		return err
	}
	if fi, err := os.Stat(s.path); err == nil {
		s.modTime = fi.ModTime()
	}
	return nil
}

// SaveSession writes a new session (e.g. from `mcp-grafana login`) while
// holding the rotation lock, so a concurrent rotation in a running server
// cannot overwrite it with the previous session.
func SaveSession(path string, sf SessionFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating session directory: %w", err)
	}
	unlock, err := lockFile(path + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	return WriteSessionFile(path, sf)
}

// WriteSessionFile atomically writes sf to path with 0600 permissions.
func WriteSessionFile(path string, sf SessionFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating session directory: %w", err)
	}
	data, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".session-*.json")
	if err != nil {
		return fmt.Errorf("writing session file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// ParseCookieHeader parses a browser "Cookie" request header value (as copied
// from DevTools, optionally still prefixed with "cookie:") into a map.
func ParseCookieHeader(raw string) map[string]string {
	raw = strings.TrimSpace(raw)
	if i := strings.Index(raw, ":"); i >= 0 && strings.EqualFold(strings.TrimSpace(raw[:i]), "cookie") {
		raw = raw[i+1:]
	}
	cookies := map[string]string{}
	for _, part := range strings.Split(raw, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || name == "" {
			continue
		}
		cookies[name] = value
	}
	return cookies
}

// CookieHeader renders cookies as a Cookie header value in a stable order.
func CookieHeader(cookies map[string]string) string {
	names := make([]string, 0, len(cookies))
	for n := range cookies {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		parts = append(parts, n+"="+cookies[n])
	}
	return strings.Join(parts, "; ")
}

// sessionRoundTripper authenticates requests with the stored browser session
// and transparently rotates it when it is due or when Grafana rejects it.
type sessionRoundTripper struct {
	store      *SessionStore
	underlying http.RoundTripper
}

func newSessionRoundTripper(rt http.RoundTripper, store *SessionStore) *sessionRoundTripper {
	if rt == nil {
		rt = http.DefaultTransport
	}
	return &sessionRoundTripper{store: store, underlying: rt}
}

func (rt *sessionRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// Make the body replayable so the request can be retried after a rotation.
	if req.Body != nil && req.Body != http.NoBody && req.GetBody == nil {
		body, err := io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			return nil, err
		}
		req = req.Clone(req.Context())
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	}

	cookie, token, needsRotation := rt.store.snapshot()
	if needsRotation {
		if err := rt.store.rotate(req.Context(), rt.underlying, token); err != nil {
			return nil, err
		}
		cookie, token, _ = rt.store.snapshot()
	}

	resp, err := rt.underlying.RoundTrip(rt.withCookie(req, cookie))
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}

	// The session may have been rotated elsewhere or have expired early.
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if err := rt.store.rotate(req.Context(), rt.underlying, token); err != nil {
		return nil, err
	}
	retry := rt.withCookie(req, "")
	if req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		retry.Body = body
	}
	cookie, _, _ = rt.store.snapshot()
	retry.Header.Set("Cookie", cookie)
	resp, err = rt.underlying.RoundTrip(retry)
	if err == nil && resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		return nil, ErrSessionExpired
	}
	return resp, err
}

func (rt *sessionRoundTripper) withCookie(req *http.Request, cookie string) *http.Request {
	clone := req.Clone(req.Context())
	if cookie != "" {
		clone.Header.Set("Cookie", cookie)
	}
	return clone
}

// NormalizeGrafanaURL trims trailing slashes and adds a scheme if missing.
func NormalizeGrafanaURL(raw string) string {
	return normalizeGrafanaURL(raw)
}

// SessionUser is the subset of /api/user returned by ValidateSession.
type SessionUser struct {
	Login string `json:"login"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

// ValidateSession checks that cookies authenticate against grafanaURL and
// returns the signed-in user.
func ValidateSession(ctx context.Context, client *http.Client, grafanaURL string, cookies map[string]string) (*SessionUser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(grafanaURL, "/")+"/api/user", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cookie", CookieHeader(cookies))
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("grafana returned %s for /api/user", resp.Status)
	}
	var u SessionUser
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return nil, fmt.Errorf("decoding /api/user: %w", err)
	}
	return &u, nil
}
