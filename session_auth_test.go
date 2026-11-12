package mcpgrafana

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSessionGrafana accepts exactly one current session token and rotates it
// on POST /api/user/auth-tokens/rotate, like Grafana's client-side rotation.
type fakeSessionGrafana struct {
	mu        sync.Mutex
	token     string
	gen       int
	revoked   bool
	rotations atomic.Int32
	bodies    []string
}

func (f *fakeSessionGrafana) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		c, err := r.Cookie(sessionCookieName)
		valid := err == nil && c.Value == f.token && !f.revoked
		switch r.URL.Path {
		case sessionRotatePath:
			if !valid {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			f.gen++
			f.token = "tok" + strconv.Itoa(f.gen)
			f.rotations.Add(1)
			http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: f.token, Path: "/", MaxAge: 2592000})
			http.SetCookie(w, &http.Cookie{Name: sessionExpiryCookieName, Value: strconv.FormatInt(time.Now().Add(10*time.Minute).Unix(), 10), Path: "/", MaxAge: 2592000})
		default:
			if !valid {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"message":"Unauthorized"}`))
				return
			}
			b, _ := io.ReadAll(r.Body)
			f.bodies = append(f.bodies, string(b))
			_, _ = w.Write([]byte(`{"login":"viewer"}`))
		}
	})
}

func newTestSession(t *testing.T, srvURL string, cookies map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.json")
	require.NoError(t, WriteSessionFile(path, SessionFile{URL: srvURL, Cookies: cookies}))
	return path
}

func TestSessionRoundTripper(t *testing.T) {
	t.Run("rotates on 401 and retries with the same body", func(t *testing.T) {
		fake := &fakeSessionGrafana{token: "tok1", gen: 1}
		srv := httptest.NewServer(fake.handler(t))
		defer srv.Close()
		path := newTestSession(t, srv.URL, map[string]string{sessionCookieName: "tok1", "AWSALB": "lb"})
		store, err := LoadSessionStore(path)
		require.NoError(t, err)

		client := &http.Client{Transport: newSessionRoundTripper(http.DefaultTransport, store)}

		// First request succeeds without rotation.
		resp, err := client.Post(srv.URL+"/api/ds/query", "application/json", strings.NewReader(`{"q":1}`))
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, int32(0), fake.rotations.Load())

		// Grafana now requires rotation: the current token is only accepted by the rotate endpoint.
		rotateOnly := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != sessionRotatePath && fake.rotations.Load() == 0 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			fake.handler(t).ServeHTTP(w, r)
		})
		srv.Config.Handler = rotateOnly

		resp, err = client.Post(srv.URL+"/api/ds/query", "application/json", strings.NewReader(`{"q":2}`))
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, int32(1), fake.rotations.Load())
		assert.Equal(t, []string{`{"q":1}`, `{"q":2}`}, fake.bodies)

		// The rotated session is persisted, other cookies are kept, file stays private.
		reloaded, err := LoadSessionStore(path)
		require.NoError(t, err)
		assert.Equal(t, "tok2", reloaded.session.Cookies[sessionCookieName])
		assert.Equal(t, "lb", reloaded.session.Cookies["AWSALB"])
		fi, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	})

	t.Run("rotates proactively once the expiry has passed", func(t *testing.T) {
		fake := &fakeSessionGrafana{token: "tok1", gen: 1}
		srv := httptest.NewServer(fake.handler(t))
		defer srv.Close()
		past := strconv.FormatInt(time.Now().Add(-time.Minute).Unix(), 10)
		path := newTestSession(t, srv.URL, map[string]string{sessionCookieName: "tok1", sessionExpiryCookieName: past})
		store, err := LoadSessionStore(path)
		require.NoError(t, err)
		client := &http.Client{Transport: newSessionRoundTripper(http.DefaultTransport, store)}

		resp, err := client.Get(srv.URL + "/api/user")
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, int32(1), fake.rotations.Load())
	})

	t.Run("picks up a rotation made by another process instead of rotating again", func(t *testing.T) {
		fake := &fakeSessionGrafana{token: "tok1", gen: 1}
		srv := httptest.NewServer(fake.handler(t))
		defer srv.Close()
		path := newTestSession(t, srv.URL, map[string]string{sessionCookieName: "tok1"})
		storeA, err := LoadSessionStore(path)
		require.NoError(t, err)
		storeB, err := LoadSessionStore(path)
		require.NoError(t, err)

		// Process A rotates.
		require.NoError(t, storeA.rotate(context.Background(), http.DefaultTransport, "tok1"))
		// Make sure the mtime differs on filesystems with coarse timestamps.
		later := time.Now().Add(time.Second)
		require.NoError(t, os.Chtimes(path, later, later))

		// Process B still holds tok1; its request must succeed using A's token.
		client := &http.Client{Transport: newSessionRoundTripper(http.DefaultTransport, storeB)}
		resp, err := client.Get(srv.URL + "/api/user")
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, int32(1), fake.rotations.Load())
	})

	t.Run("returns ErrSessionExpired when the session was revoked", func(t *testing.T) {
		fake := &fakeSessionGrafana{token: "tok1", gen: 1, revoked: true}
		srv := httptest.NewServer(fake.handler(t))
		defer srv.Close()
		path := newTestSession(t, srv.URL, map[string]string{sessionCookieName: "tok1"})
		store, err := LoadSessionStore(path)
		require.NoError(t, err)
		client := &http.Client{Transport: newSessionRoundTripper(http.DefaultTransport, store)}

		_, err = client.Get(srv.URL + "/api/user")
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrSessionExpired), "got %v", err)
	})
}

func TestBuildTransportSession(t *testing.T) {
	fake := &fakeSessionGrafana{token: "tok1", gen: 1}
	srv := httptest.NewServer(fake.handler(t))
	defer srv.Close()
	store, err := LoadSessionStore(newTestSession(t, srv.URL, map[string]string{sessionCookieName: "tok1"}))
	require.NoError(t, err)

	t.Run("session cookie is sent when no other credentials are set", func(t *testing.T) {
		rt, err := BuildTransport(&GrafanaConfig{Session: store}, nil)
		require.NoError(t, err)
		resp, err := (&http.Client{Transport: rt}).Get(srv.URL + "/api/user")
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("explicit credentials take precedence over the session", func(t *testing.T) {
		rt, err := BuildTransport(&GrafanaConfig{Session: store, APIKey: "sa-token"}, nil)
		require.NoError(t, err)
		resp, err := (&http.Client{Transport: rt}).Get(srv.URL + "/api/user")
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})
}

func TestParseCookieHeader(t *testing.T) {
	got := ParseCookieHeader("cookie: _ga=GA1.2=3; grafana_session=abc; grafana_session_expiry=123\n")
	assert.Equal(t, map[string]string{"_ga": "GA1.2=3", "grafana_session": "abc", "grafana_session_expiry": "123"}, got)
	assert.Equal(t, "_ga=GA1.2=3; grafana_session=abc; grafana_session_expiry=123", CookieHeader(got))
}

func TestSaveSessionWaitsForRotationLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	unlock, err := lockFile(path + ".lock")
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() {
		done <- SaveSession(path, SessionFile{URL: "https://g", Cookies: map[string]string{sessionCookieName: "new"}})
	}()
	select {
	case <-done:
		t.Fatal("SaveSession must not write while a rotation holds the lock")
	case <-time.After(100 * time.Millisecond):
	}
	unlock()
	require.NoError(t, <-done)
	store, err := LoadSessionStore(path)
	require.NoError(t, err)
	assert.Equal(t, "new", store.session.Cookies[sessionCookieName])
}
