package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"

	mcpgrafana "github.com/grafana/mcp-grafana/v2"
)

const loginUsage = `Usage: mcp-grafana login [flags]

Sign in to Grafana through the browser (SSO, MFA, ...) and store the resulting
browser session so the MCP server can use it instead of a service account token.

By default Chrome is opened with a dedicated profile; sign in, press Enter,
and the session is read from that profile. With --paste, your default browser is
opened and you paste the Cookie request header copied from DevTools instead.

Then run the server with GRAFANA_SESSION_FILE pointing at the session file
(stdio transport only). The server rotates the session automatically; sign in
again only when Grafana ends it.

For Cursor cloud agents, set CURSOR_CLOUD=true and put the contents of the
session file (not its path) into the GRAFANA_SESSION_FILE personal secret.

Guide: docs/browser-session-login.md

Flags:
`

// runLogin implements the `mcp-grafana login` subcommand and returns the exit code.
func runLogin(args []string) int {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), loginUsage)
		fs.PrintDefaults()
	}
	grafanaURL := fs.String("url", os.Getenv("GRAFANA_URL"), "Grafana URL (defaults to GRAFANA_URL)")
	sessionFile := fs.String("session-file", "", "Where to store the session (defaults to GRAFANA_SESSION_FILE or "+mcpgrafana.DefaultSessionFilePath()+")")
	paste := fs.Bool("paste", false, "Paste the Cookie header from your own browser instead of opening a controlled Chrome window")
	browser := fs.String("browser", "", "Path to a Chrome/Chromium/Edge executable (auto-detected if empty)")
	timeout := fs.Duration("timeout", 5*time.Minute, "How long to wait for the browser login to complete")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	path := *sessionFile
	if path == "" {
		path = mcpgrafana.SessionFilePathFromEnv()
	}
	if path == "" {
		path = mcpgrafana.DefaultSessionFilePath()
	}

	u := mcpgrafana.NormalizeGrafanaURL(*grafanaURL)
	if u == "" {
		// Re-use the URL of an existing session, if any.
		if store, err := mcpgrafana.LoadSessionStore(path); err == nil {
			u = store.URL()
		}
	}
	if u == "" {
		fmt.Fprintln(os.Stderr, "Grafana URL is required: pass --url or set GRAFANA_URL")
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var (
		cookies map[string]string
		user    *mcpgrafana.SessionUser
		err     error
	)
	if *paste {
		cookies, user, err = loginPaste(ctx, u)
	} else {
		cookies, user, err = loginBrowser(ctx, u, *browser, filepath.Join(filepath.Dir(path), "browser-profile"), *timeout)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "login failed: %v\n", err)
		return 1
	}

	sf := mcpgrafana.SessionFile{URL: u, Cookies: cookies, UpdatedAt: time.Now().UTC()}
	if err := mcpgrafana.SaveSession(path, sf); err != nil {
		fmt.Fprintf(os.Stderr, "saving session: %v\n", err)
		return 1
	}

	fmt.Printf("Signed in to %s as %s.\nSession saved to %s\n\n", u, describeUser(user), path)
	fmt.Printf("Start the MCP server with:\n  GRAFANA_SESSION_FILE=%s mcp-grafana\n", path)
	return 0
}

func describeUser(u *mcpgrafana.SessionUser) string {
	switch {
	case u.Email != "" && u.Login != "" && u.Email != u.Login:
		return fmt.Sprintf("%s (%s)", u.Login, u.Email)
	case u.Login != "":
		return u.Login
	default:
		return u.Email
	}
}

// loginBrowser signs in through a dedicated Chrome profile. Identity providers
// such as Google refuse sign-in from automated browsers, so Chrome is started
// as a plain process for the interactive part; automation (headless, against
// the same profile) is only used before and after it to clear and read the
// Grafana cookies. The profile is kept between runs so the identity provider
// can remember you.
func loginBrowser(ctx context.Context, grafanaURL, browserPath, profileDir string, timeout time.Duration) (map[string]string, *mcpgrafana.SessionUser, error) {
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("creating browser profile: %w", err)
	}
	execPath, err := findBrowser(browserPath)
	if err != nil {
		return nil, nil, err
	}

	// Forget any previous Grafana session in the profile so we capture a fresh
	// one; identity-provider cookies are kept so SSO can be quick.
	if err := withHeadlessProfile(ctx, execPath, profileDir, func(bctx context.Context) error {
		for _, name := range []string{"grafana_session", "grafana_session_expiry"} {
			if _, err := chromedp.Call(bctx, network.DeleteCookies, network.DeleteCookiesParams{Name: name, URL: grafanaURL}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, nil, fmt.Errorf("preparing browser profile (is another login still running?): %w", err)
	}

	cmd := exec.Command(execPath,
		"--user-data-dir="+profileDir,
		"--no-first-run",
		"--no-default-browser-check",
		"--new-window",
		grafanaURL+"/login",
	)
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("starting browser %s: %w", execPath, err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()

	fmt.Fprintf(os.Stderr, "A browser window has opened. Sign in to %s there.\nOnce you see Grafana, press Enter here, or quit that browser (Cmd+Q on macOS).\n", grafanaURL)
	enter := make(chan struct{})
	go func() {
		// Only a real Enter counts. When stdin is closed or empty (for example
		// when a coding agent runs this command), ReadString returns EOF at
		// once; then we wait for the browser to be quit instead of giving up
		// before the user has signed in.
		if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err == nil {
			close(enter)
		}
	}()

	var waitErr error
	select {
	case <-exited:
	case <-enter:
	case <-ctx.Done():
		waitErr = ctx.Err()
	case <-time.After(timeout):
		waitErr = fmt.Errorf("timed out after %s waiting for sign-in", timeout)
	}
	// Ask Chrome to shut down cleanly so it flushes its cookie store to disk.
	select {
	case <-exited:
	default:
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			_ = cmd.Process.Kill()
		}
		select {
		case <-exited:
		case <-time.After(15 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
		}
	}
	if waitErr != nil {
		return nil, nil, waitErr
	}

	var cookies map[string]string
	if err := withHeadlessProfile(ctx, execPath, profileDir, func(bctx context.Context) error {
		res, err := chromedp.Call(bctx, network.GetCookies, network.GetCookiesParams{URLs: []string{grafanaURL + "/"}})
		if err != nil {
			return err
		}
		cookies = map[string]string{}
		for _, c := range res.Cookies {
			cookies[c.Name] = c.Value
		}
		return nil
	}); err != nil {
		return nil, nil, fmt.Errorf("reading cookies from browser profile: %w", err)
	}
	if cookies["grafana_session"] == "" {
		return nil, nil, errors.New("no Grafana session found in the browser; make sure sign-in finished before pressing Enter (or use --paste)")
	}
	user, err := mcpgrafana.ValidateSession(ctx, &http.Client{Timeout: 15 * time.Second}, grafanaURL, cookies)
	if err != nil {
		return nil, nil, fmt.Errorf("captured session was rejected: %w", err)
	}
	return cookies, user, nil
}

// withHeadlessProfile runs f against a headless Chrome using profileDir and
// shuts it down cleanly afterwards so cookie changes are persisted.
func withHeadlessProfile(ctx context.Context, execPath, profileDir string, f func(context.Context) error) error {
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx,
		chromedp.ExecPath(execPath),
		chromedp.UserDataDir(profileDir),
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.Flag("headless", true),
	)
	defer cancelAlloc()
	bctx, cancelBrowser := chromedp.NewContext(allocCtx)
	defer cancelBrowser()
	if err := f(bctx); err != nil {
		return err
	}
	return chromedp.Cancel(bctx)
}

// findBrowser returns the Chrome/Chromium/Edge executable to use.
func findBrowser(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	var candidates []string
	switch runtime.GOOS {
	case "darwin":
		candidates = []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
		}
	case "windows":
		candidates = []string{
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
			"chrome.exe", "msedge.exe",
		}
	default:
		candidates = []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "microsoft-edge"}
	}
	for _, c := range candidates {
		if p, err := exec.LookPath(c); err == nil {
			return p, nil
		}
	}
	return "", errors.New("no Chrome, Chromium or Edge found; pass --browser or use --paste")
}

// loginPaste opens the default browser on Grafana and asks the user to paste
// the Cookie request header from DevTools.
func loginPaste(ctx context.Context, grafanaURL string) (map[string]string, *mcpgrafana.SessionUser, error) {
	if err := openURL(grafanaURL); err != nil {
		fmt.Fprintf(os.Stderr, "Could not open a browser (%v); open %s yourself.\n", err, grafanaURL)
	}
	fmt.Fprintf(os.Stderr, `Sign in to %s in your browser, then:
  1. Open DevTools (Cmd+Opt+I / F12) -> Network, and reload the page.
  2. Click any request to /api/..., find "cookie" under Request Headers and copy its value.
  3. Paste it here and press Enter.
Tip: sign in from a private window and close it afterwards, so the browser does not rotate the session away.

Cookie: `, grafanaURL)

	line, err := bufio.NewReaderSize(os.Stdin, 64*1024).ReadString('\n')
	if err != nil && line == "" {
		return nil, nil, fmt.Errorf("reading cookie: %w", err)
	}
	cookies := mcpgrafana.ParseCookieHeader(line)
	if cookies["grafana_session"] == "" {
		return nil, nil, errors.New("the pasted value has no grafana_session cookie")
	}
	user, err := mcpgrafana.ValidateSession(ctx, &http.Client{Timeout: 15 * time.Second}, grafanaURL, cookies)
	if err != nil {
		return nil, nil, fmt.Errorf("%w (the cookie may already have been rotated by the browser; copy a fresh one)", err)
	}
	return cookies, user, nil
}

func openURL(u string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", u).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start()
	default:
		return exec.Command("xdg-open", u).Start()
	}
}

// configureSessionAuth enables browser-session auth when GRAFANA_SESSION_FILE
// is set and no other Grafana credentials are configured.
func configureSessionAuth(cfg *mcpgrafana.GrafanaConfig, transport string) error {
	path := mcpgrafana.SessionFilePathFromEnv()
	if path == "" {
		return nil
	}
	for _, v := range []string{"GRAFANA_SERVICE_ACCOUNT_TOKEN", "GRAFANA_SERVICE_ACCOUNT_TOKEN_FILE", "GRAFANA_API_KEY", "GRAFANA_USERNAME"} {
		if os.Getenv(v) != "" {
			return fmt.Errorf("GRAFANA_SESSION_FILE cannot be combined with %s", v)
		}
	}
	if transport != "stdio" {
		// A session is a personal login; never expose it to remote callers.
		return errors.New("GRAFANA_SESSION_FILE is only supported with the stdio transport")
	}
	// Under CURSOR_CLOUD the variable holds the session JSON itself, and
	// rotated sessions are saved back to the user's personal Cursor secret.
	cloud := mcpgrafana.CursorCloudEnabled()
	var (
		store *mcpgrafana.SessionStore
		err   error
		src   = path
	)
	if cloud {
		src = "GRAFANA_SESSION_FILE"
		store, err = mcpgrafana.LoadSessionStoreFromValue(path, mcpgrafana.PersistSessionToCursorSecret)
	} else {
		store, err = mcpgrafana.LoadSessionStore(path)
	}
	if err != nil {
		return err
	}
	envURL := mcpgrafana.NormalizeGrafanaURL(os.Getenv("GRAFANA_URL"))
	switch {
	case envURL == "":
		if err := os.Setenv("GRAFANA_URL", store.URL()); err != nil {
			return err
		}
	case envURL != mcpgrafana.NormalizeGrafanaURL(store.URL()):
		return fmt.Errorf("GRAFANA_URL (%s) does not match the URL of the session in %s (%s)", envURL, src, store.URL())
	}
	cfg.Session = store
	return nil
}
