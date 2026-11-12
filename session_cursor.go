package mcpgrafana

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// cursorAPIBaseURL is the Cursor API root. It is a variable so tests can point
// it at a fake server.
var cursorAPIBaseURL = "https://api.cursor.com"

// PersistSessionToCursorSecret writes sf back to the GRAFANA_SESSION_FILE
// personal secret of the Cursor user that owns CURSOR_API_KEY, via
// PUT /v1/me/secrets/{name}. Agents started after the write receive the new
// value. An environment secret with the same name would take precedence, so
// it must not be set.
func PersistSessionToCursorSecret(sf SessionFile) error {
	apiKey := os.Getenv("CURSOR_API_KEY")
	if apiKey == "" {
		return errors.New("CURSOR_API_KEY must be set to save the rotated Grafana session")
	}
	data, err := json.Marshal(sf)
	if err != nil {
		return err
	}
	return putPersonalSecret(cursorAPIBaseURL, apiKey, grafanaSessionFileEnvVar, string(data))
}

// putPersonalSecret sets the value of one personal secret. The API returns 201
// when the secret is created and 200 when it is changed; both are success.
func putPersonalSecret(baseURL, apiKey, name, value string) error {
	body, err := json.Marshal(map[string]string{"value": value})
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("%s/v1/me/secrets/%s", strings.TrimRight(baseURL, "/"), url.PathEscape(name))
	req, err := http.NewRequest(http.MethodPut, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.SetBasicAuth(apiKey, "")
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("updating Cursor secret %s: %w", name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("updating Cursor secret %s: unexpected status %s", name, resp.Status)
	}
	return nil
}
