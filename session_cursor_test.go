package mcpgrafana

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCursorCloudEnabled(t *testing.T) {
	t.Setenv(cursorCloudEnvVar, "true")
	assert.True(t, CursorCloudEnabled())
	t.Setenv(cursorCloudEnvVar, " True ")
	assert.True(t, CursorCloudEnabled())
	t.Setenv(cursorCloudEnvVar, "false")
	assert.False(t, CursorCloudEnabled())
	t.Setenv(cursorCloudEnvVar, "")
	assert.False(t, CursorCloudEnabled())
}

func TestLoadSessionStoreFromValue(t *testing.T) {
	t.Run("rotates in memory and hands the new session to persist", func(t *testing.T) {
		fake := &fakeSessionGrafana{token: "tok1", gen: 1}
		srv := httptest.NewServer(fake.handler(t))
		defer srv.Close()
		past := strconv.FormatInt(time.Now().Add(-time.Minute).Unix(), 10)
		value, err := json.Marshal(SessionFile{URL: srv.URL, Cookies: map[string]string{sessionCookieName: "tok1", sessionExpiryCookieName: past}})
		require.NoError(t, err)

		var persisted []SessionFile
		store, err := LoadSessionStoreFromValue(string(value), func(sf SessionFile) error {
			persisted = append(persisted, sf)
			return nil
		})
		require.NoError(t, err)
		client := &http.Client{Transport: newSessionRoundTripper(http.DefaultTransport, store)}

		resp, err := client.Get(srv.URL + "/api/user")
		require.NoError(t, err)
		_ = resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, int32(1), fake.rotations.Load())
		require.Len(t, persisted, 1)
		assert.Equal(t, "tok2", persisted[0].Cookies[sessionCookieName])
	})

	t.Run("returns an error when persisting the rotated session fails", func(t *testing.T) {
		fake := &fakeSessionGrafana{token: "tok1", gen: 1}
		srv := httptest.NewServer(fake.handler(t))
		defer srv.Close()
		past := strconv.FormatInt(time.Now().Add(-time.Minute).Unix(), 10)
		value, err := json.Marshal(SessionFile{URL: srv.URL, Cookies: map[string]string{sessionCookieName: "tok1", sessionExpiryCookieName: past}})
		require.NoError(t, err)

		store, err := LoadSessionStoreFromValue(string(value), func(SessionFile) error {
			return errors.New("secret store down")
		})
		require.NoError(t, err)
		client := &http.Client{Transport: newSessionRoundTripper(http.DefaultTransport, store)}

		_, err = client.Get(srv.URL + "/api/user")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "secret store down")
	})

	t.Run("rejects a value without a session cookie or without JSON", func(t *testing.T) {
		_, err := LoadSessionStoreFromValue(`{"url":"https://g","cookies":{}}`, func(SessionFile) error { return nil })
		require.Error(t, err)
		_, err = LoadSessionStoreFromValue("/path/to/session.json", func(SessionFile) error { return nil })
		require.Error(t, err)
	})
}

func TestPersistSessionToCursorSecret(t *testing.T) {
	var (
		gotMethod, gotPath, gotUser, gotPass, gotType string
		gotValue                                      string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotType = r.Method, r.URL.Path, r.Header.Get("Content-Type")
		gotUser, gotPass, _ = r.BasicAuth()
		var body struct {
			Value string `json:"value"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		gotValue = body.Value
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	orig := cursorAPIBaseURL
	cursorAPIBaseURL = srv.URL
	t.Cleanup(func() { cursorAPIBaseURL = orig })
	t.Setenv("CURSOR_API_KEY", "key-123")

	sf := SessionFile{URL: "https://grafana.example", Cookies: map[string]string{sessionCookieName: "tok9"}}
	require.NoError(t, PersistSessionToCursorSecret(sf))

	assert.Equal(t, http.MethodPut, gotMethod)
	assert.Equal(t, "/v1/me/secrets/GRAFANA_SESSION_FILE", gotPath)
	assert.Equal(t, "key-123", gotUser)
	assert.Empty(t, gotPass)
	assert.Equal(t, "application/json", gotType)

	var roundTripped SessionFile
	require.NoError(t, json.Unmarshal([]byte(gotValue), &roundTripped))
	assert.Equal(t, sf.URL, roundTripped.URL)
	assert.Equal(t, "tok9", roundTripped.Cookies[sessionCookieName])
}

func TestPersistSessionToCursorSecretErrors(t *testing.T) {
	t.Run("requires the API key", func(t *testing.T) {
		t.Setenv("CURSOR_API_KEY", "")
		assert.Error(t, PersistSessionToCursorSecret(SessionFile{}))
	})

	t.Run("reports a non-2xx response", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer srv.Close()
		orig := cursorAPIBaseURL
		cursorAPIBaseURL = srv.URL
		t.Cleanup(func() { cursorAPIBaseURL = orig })
		t.Setenv("CURSOR_API_KEY", "key")

		err := PersistSessionToCursorSecret(SessionFile{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "403")
	})
}
