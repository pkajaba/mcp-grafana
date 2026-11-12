//go:build windows

package mcpgrafana

// lockFile is a no-op on Windows: concurrent processes sharing one session
// file may race on rotation, and the loser simply retries after a 401.
func lockFile(string) (func(), error) {
	return func() {}, nil
}
