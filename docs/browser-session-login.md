# Sign in with your Grafana browser session

This fork lets `mcp-grafana` act as **you**, using the session you get when you
sign in to Grafana in a browser (SSO, MFA and so on), instead of a service
account token.

That is useful when:

- you can't get a service account token, or getting one takes a ticket;
- you want the MCP server to see exactly what you can see in Grafana, no more
  and no less;
- your Grafana only allows sign-in through an identity provider.

The server keeps the session alive on its own by rotating it, the same way the
Grafana web UI does. You only sign in again when Grafana ends the session
(see [How long a session lasts](#how-long-a-session-lasts)).

> [!TIP]
> To let a coding agent such as Claude Code, Cursor or Codex install and
> configure this for you, point it at [Agent setup](agent-setup.md).

- [Quick start](#quick-start)
- [Install](#install)
- [Sign in](#sign-in)
- [Configure your MCP client](#configure-your-mcp-client)
- [Cursor cloud agents](#cursor-cloud-agents)
- [Docker](#docker)
- [How it works](#how-it-works)
- [Security notes](#security-notes)
- [Troubleshooting](#troubleshooting)
- [Reference](#reference)

## Quick start

```bash
# 1. Download the binary for your OS and CPU (macOS or Linux)
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
mkdir -p "$HOME/bin"
curl -fsSL -o "$HOME/bin/mcp-grafana" \
  "https://github.com/pkajaba/mcp-grafana/releases/latest/download/mcp-grafana_${OS}_${ARCH}"
chmod +x "$HOME/bin/mcp-grafana"

# 2. Sign in (opens a Chrome window)
"$HOME/bin/mcp-grafana" login --url https://yourcompany.grafana.net
```

[Install](#install) shows how to verify the download.

Then point your MCP client at the binary and the session file:

```json
{
  "mcpServers": {
    "grafana": {
      "command": "/Users/you/bin/mcp-grafana",
      "env": {
        "GRAFANA_SESSION_FILE": "/Users/you/.config/mcp-grafana/session.json"
      }
    }
  }
}
```

That's it. `GRAFANA_URL` is optional: the server takes it from the session file.

## Install

The `login` command and session support exist only in this fork, so
`uvx mcp-grafana`, the Docker images and the upstream releases **won't work**.
Use this fork's [releases](https://github.com/pkajaba/mcp-grafana/releases),
which have binaries for macOS and Linux on amd64 and arm64.

For browser sign-in you also need Google Chrome, Chromium or Microsoft Edge (on
macOS, Brave is also detected). Without one you can use [paste mode](#paste-mode).

### Download a release

This downloads the latest release for your OS and CPU, checks it against the
published checksums, and installs it as `~/bin/mcp-grafana`:

```bash
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
FILE="mcp-grafana_${OS}_${ARCH}"
BASE=https://github.com/pkajaba/mcp-grafana/releases/latest/download
TMP=$(mktemp -d)
curl -fsSL -o "$TMP/$FILE" "$BASE/$FILE" \
  && curl -fsSL -o "$TMP/checksums.txt" "$BASE/checksums.txt" \
  && (cd "$TMP" && grep " $FILE\$" checksums.txt | {
        if command -v sha256sum >/dev/null; then sha256sum -c -; else shasum -a 256 -c -; fi; }) \
  && mkdir -p "$HOME/bin" \
  && install -m 755 "$TMP/$FILE" "$HOME/bin/mcp-grafana" \
  && "$HOME/bin/mcp-grafana" --version
rm -rf "$TMP"
```

To update, run the same commands again and restart your MCP client.

The rest of this guide runs `mcp-grafana` by name. If `$HOME/bin` isn't on your
`PATH` (it often isn't on macOS), add it, for example in `~/.zshrc`:

```bash
export PATH="$HOME/bin:$PATH"
```

Or type the full path, `$HOME/bin/mcp-grafana`. MCP client configs always need
the full path.

The binaries are built by a
[GitHub Actions workflow](../.github/workflows/fork-release.yml) from the tagged
commit, with signed build provenance. With the
[GitHub CLI](https://cli.github.com/) you can check that a binary came from that
workflow:

```bash
gh attestation verify "$HOME/bin/mcp-grafana" -R pkajaba/mcp-grafana
```

The macOS binaries aren't signed by Apple. A file downloaded with `curl`, as
above, runs as is. If you download it in a browser instead, macOS blocks it.
Remove the quarantine flag with `xattr -d com.apple.quarantine mcp-grafana`.

### Build from source

For Windows, or to build a change yourself, you need Go at the version in
[`go.mod`](../go.mod):

```bash
git clone https://github.com/pkajaba/mcp-grafana.git
cd mcp-grafana
go build -o "$HOME/bin/mcp-grafana" ./cmd/mcp-grafana
```

### Making a release

For maintainers: push a tag such as `v2.3.0-session.2`. The upstream version
comes first, then the fork's release number.
[`fork-release.yml`](../.github/workflows/fork-release.yml) builds the binaries
with [`.goreleaser.fork.yaml`](../.goreleaser.fork.yaml), attests them and
publishes the release. Upstream's own workflows are disabled in this fork.

## Sign in

### Browser mode (default)

```bash
mcp-grafana login --url https://yourcompany.grafana.net
```

1. A new Chrome window opens on the Grafana login page. It uses its own profile,
   `browser-profile/` next to the session file (by default
   `~/.config/mcp-grafana/browser-profile`), separate from your normal browser.
   The profile remembers your identity provider sign-in, so next time is quicker.
2. Sign in as usual, including SSO and MFA.
3. Once Grafana has loaded, go back to the terminal and press **Enter**, or quit
   that Chrome window (Cmd+Q on macOS). When stdin isn't a terminal, for example
   when an agent runs the command, quitting the browser is the only way to finish.
4. The command checks the session against `/api/user` and saves it:

   ```text
   Signed in to https://yourcompany.grafana.net as jane (jane@example.com).
   Session saved to /Users/jane/.config/mcp-grafana/session.json
   ```

The dedicated profile remembers your identity provider between runs, so signing
in again later is usually one or two clicks.

Chrome is started as a normal process for the sign-in itself, because providers
such as Google refuse sign-in from automated browsers. Automation (headless
Chrome on the same profile) is used only before and after, to clear the old
Grafana cookies and to read the new ones.

### Paste mode

Use this when no supported browser is installed, or when browser mode doesn't
work for your setup:

```bash
mcp-grafana login --url https://yourcompany.grafana.net --paste
```

1. Your default browser opens on Grafana. **Use a private/incognito window**
   if you can (see the note below).
2. Sign in, open DevTools (**Cmd+Opt+I** on macOS, **F12** elsewhere), go to
   **Network**, and reload the page.
3. Click any request to `/api/...`, find `cookie` under **Request Headers**,
   and copy its value.
4. Paste it into the terminal and press Enter.

> [!IMPORTANT]
> Close that browser window once the command finishes. Grafana rotates the
> session token every few minutes. If the browser and the MCP server both keep
> using the same session, whichever rotates first logs the other one out.

### Login flags

| Flag             | Default                                                       | Description                                                  |
| :--------------- | :------------------------------------------------------------ | :----------------------------------------------------------- |
| `--url`          | `GRAFANA_URL`, or the URL in an existing session file          | Grafana URL to sign in to.                                   |
| `--session-file` | `GRAFANA_SESSION_FILE`, or `~/.config/mcp-grafana/session.json` | Where to save the session.                                   |
| `--paste`        | off                                                           | Paste the Cookie header instead of opening Chrome.           |
| `--browser`      | auto-detected                                                 | Path to a Chrome, Chromium or Edge executable.               |
| `--timeout`      | `5m`                                                          | How long to wait for you to finish signing in.               |

`mcp-grafana login --help` shows a short version of this list.

## Configure your MCP client

Set `GRAFANA_SESSION_FILE` to the **absolute** path of the session file. `~` and
`$HOME` are not expanded inside most MCP client configs.

Don't also set `GRAFANA_SERVICE_ACCOUNT_TOKEN`, `GRAFANA_SERVICE_ACCOUNT_TOKEN_FILE`,
`GRAFANA_API_KEY` or `GRAFANA_USERNAME`. The server refuses to start if you
combine them with a session.

Session auth works only with the default **stdio** transport. The server refuses
to start with `-t sse` or `-t streamable-http`, so a personal login is never
exposed to remote callers.

### Claude Desktop and Cursor

Claude Desktop: `~/Library/Application Support/Claude/claude_desktop_config.json`
(macOS) or `%APPDATA%\Claude\claude_desktop_config.json` (Windows).
Cursor: `~/.cursor/mcp.json`, or `.cursor/mcp.json` in a project.

```json
{
  "mcpServers": {
    "grafana": {
      "command": "/Users/you/bin/mcp-grafana",
      "env": {
        "GRAFANA_SESSION_FILE": "/Users/you/.config/mcp-grafana/session.json"
      }
    }
  }
}
```

All the usual upstream options still apply. For example, add `"args": ["--disable-write"]`
to keep the server read-only.

### Claude Code

```bash
claude mcp add --scope user \
  --env GRAFANA_SESSION_FILE="$HOME/.config/mcp-grafana/session.json" \
  grafana -- "$HOME/bin/mcp-grafana"
```

`--scope user` makes it available in every project. Without it, Claude Code
adds the server only for the current directory.

### Codex

```bash
codex mcp add grafana \
  --env GRAFANA_SESSION_FILE="$HOME/.config/mcp-grafana/session.json" \
  -- "$HOME/bin/mcp-grafana"
```

Or in `~/.codex/config.toml`:

```toml
[mcp_servers.grafana]
command = "/Users/you/bin/mcp-grafana"
args = []
env = { GRAFANA_SESSION_FILE = "/Users/you/.config/mcp-grafana/session.json" }
```

### Several Grafana instances

Sign in once per instance, each to its own file, and add one MCP server per
file:

```bash
mcp-grafana login --url https://prod.grafana.example   --session-file ~/.config/mcp-grafana/prod.json
mcp-grafana login --url https://staging.grafana.example --session-file ~/.config/mcp-grafana/staging.json
```

Several MCP servers (for example Claude Desktop and Cursor at the same time)
can share **one** session file. They coordinate through a lock file, so only one
of them rotates the session and the others pick up the new token. This
coordination is not available on Windows; see [Reference](#reference).

### Separate sessions: your browser, and one per agent

Every Grafana sign-in creates its own session with its own token, and each
session rotates independently. Two holders of the **same** session compete,
because whichever rotates first logs out the other. Holders of **different**
sessions never affect each other. You can see them all, and revoke each one,
under **Profile → Sessions** in Grafana.

- **Browser mode doesn't compete with your normal browser.** It signs in through
  its own Chrome profile, so Grafana creates a new session for the MCP server.
  Keep using Grafana in your everyday browser as usual.
- **Paste mode shares a session with the browser you copied it from.** That's
  why it tells you to use a private window and close it afterwards.
- **To get several independent sessions,** for example one per cloud agent, run
  `login` once per session, each with its own `--session-file`. Each run first
  clears the Grafana cookies in the dedicated profile, so you sign in again and
  Grafana issues a new session. With SSO this is usually a single click. The
  earlier sessions stay valid, because clearing the cookie in the profile
  doesn't sign them out.

  ```bash
  mcp-grafana login --url https://yourcompany.grafana.net --session-file ~/.config/mcp-grafana/agent-1.json
  mcp-grafana login --url https://yourcompany.grafana.net --session-file ~/.config/mcp-grafana/agent-2.json
  ```

## Cursor cloud agents

> [!WARNING]
> **Cloud agents need a Grafana that is reachable from the internet.** They run
> on Cursor's servers, so they can't connect to a Grafana that is only reachable
> over a VPN or a private network. In that case, use a local client (Claude
> Code, Codex, Cursor or Claude Desktop) instead.

Cloud agents can't open a browser or read a file from your laptop, so in cloud
mode the session JSON itself goes into a Cursor secret, and every rotation is
written back to that secret.

Turn this on with `CURSOR_CLOUD=true`. In that mode:

- `GRAFANA_SESSION_FILE` holds the **contents** of the session file, not a path;
- the session lives only in memory while the server runs;
- after each rotation the server writes the new session to your **personal**
  Cursor secret `GRAFANA_SESSION_FILE`, using
  [`PUT /v1/me/secrets/GRAFANA_SESSION_FILE`](https://cursor.com/docs/cloud-agent/api/endpoints#set-a-personal-secret).

### Set it up

1. On your machine, sign in and check the size of the session file:

   ```bash
   mcp-grafana login --url https://yourcompany.grafana.net
   wc -c ~/.config/mcp-grafana/session.json
   ```

   Cursor secrets are limited to **4,096 bytes**. If the file is bigger, sign in
   with `--paste` and copy only the Grafana cookies you need (at least
   `grafana_session` and `grafana_session_expiry`).

2. In Cursor, add two **personal** secrets:

   | Name                   | Value                                                                    |
   | :--------------------- | :----------------------------------------------------------------------- |
   | `GRAFANA_SESSION_FILE` | The full contents of `session.json`                                       |
   | `CURSOR_API_KEY`       | A Cursor API key that belongs to **you**. A service account key won't work. |

3. Make sure there is **no environment secret** called `GRAFANA_SESSION_FILE`.
   Environment secrets override personal ones, so the agent would keep getting
   the stale value.

4. In the cloud agent's environment, install the fork's binary (see
   [Install](#install)) and run it with `CURSOR_CLOUD=true`. However your
   environment declares MCP servers, the entry looks like this:

   ```json
   {
     "mcpServers": {
       "grafana": {
         "command": "mcp-grafana",
         "env": { "CURSOR_CLOUD": "true" }
       }
     }
   }
   ```

   `GRAFANA_SESSION_FILE` and `CURSOR_API_KEY` reach the process from the
   secrets. `GRAFANA_URL` is optional and is read from the session.

### Things to know

- **New agents get the latest session.** Cursor gives an agent the secret values
  at start-up. A running agent keeps its own in-memory copy, which is fine,
  because it rotates that copy itself.
- **Avoid several agents at once on the same session.** Each agent has its own
  copy. When one rotates, the others' copies can go stale, and they fail with
  `session expired` until they are restarted and pick up the latest secret.
  Parallel agents would need [separate sessions](#separate-sessions-your-browser-and-one-per-agent).
  Cloud mode doesn't support that yet, because it writes to a single
  `GRAFANA_SESSION_FILE` secret.
- **A failed write-back fails the request.** If the server can't save the
  rotated session to Cursor, the request returns that error. The agent keeps
  working with its in-memory session, but the saved secret is now stale. If an
  agent restarts after this, sign in again and update the secret.

## Docker

The images built from upstream don't include this feature. If you build your own
image from this fork, sign in on the host and mount the directory that holds the
session file. Mount it **read-write**, because the server rewrites the file and
creates a lock file next to it on every rotation:

```bash
docker run --rm -i \
  -v "$HOME/.config/mcp-grafana:/session" \
  -e GRAFANA_SESSION_FILE=/session/session.json \
  your-mcp-grafana-image -t stdio
```

## How it works

1. `mcp-grafana login` stores the Grafana cookies (`grafana_session`,
   `grafana_session_expiry`, plus any load balancer cookies) and the Grafana URL
   in a JSON file with `0600` permissions:

   ```json
   {
     "url": "https://yourcompany.grafana.net",
     "cookies": {
       "grafana_session": "…",
       "grafana_session_expiry": "1762862400"
     },
     "updated_at": "2026-11-12T09:00:00Z"
   }
   ```

2. The server sends those cookies with every Grafana API request.
3. When `grafana_session_expiry` has passed, or Grafana answers `401`, the server
   calls `POST <url>/api/user/auth-tokens/rotate`, the same endpoint the Grafana
   web UI uses, then saves the new cookies and retries the request once.
4. If the rotate call itself returns `401` or `403`, the session is over and the
   server returns
   ``grafana browser session expired or was revoked; run `mcp-grafana login` to sign in again``.

### How long a session lasts

Your Grafana server decides this, not `mcp-grafana`. The relevant Grafana
settings are `login_maximum_inactive_lifetime_duration` (7 days by default) and
`login_maximum_lifetime_duration` (30 days by default). Your Grafana
administrator may have set them differently.

A session also ends if you sign out of Grafana, or revoke the session under
**Profile → Sessions**.

## Security notes

- **The server has your permissions.** Anything you can do in Grafana, an agent
  using this server can do. Consider `--disable-write`, or the other
  `--disable-*` flags, to limit it.
- **Treat the session file like a password.** It is created with `0600`
  permissions. Don't commit it, paste it into chats, or share it. Anyone holding
  it can act as you until the session ends.
- **To revoke access**, sign out of Grafana or revoke the session under
  **Profile → Sessions**, then delete the session file.
- **stdio only.** The server refuses to serve a personal session over SSE or
  streamable HTTP.

## Troubleshooting

### ``grafana browser session expired or was revoked; run `mcp-grafana login` to sign in again``

The session reached its lifetime, was revoked, or was rotated away by a browser
using the same session. Run `mcp-grafana login` again. In Cursor cloud mode,
also update the `GRAFANA_SESSION_FILE` personal secret.

### `rotating grafana session: net/http: TLS handshake timeout`

The server couldn't connect to Grafana to rotate the session. This is a network
problem, not a bad URL path. Rotation goes to the `url` in the session file,
plus `/api/user/auth-tokens/rotate`. Check from the same machine or environment:

```bash
curl -v https://yourcompany.grafana.net/api/health
```

If that also hangs, look at the VPN, proxy or firewall. If you route Grafana
traffic through a proxy, check `GRAFANA_SOCKS5_PROXY` or `HTTPS_PROXY`.

### `GRAFANA_SESSION_FILE cannot be combined with GRAFANA_SERVICE_ACCOUNT_TOKEN`

Remove the other credential variable from the MCP client config. The same
applies to `GRAFANA_SERVICE_ACCOUNT_TOKEN_FILE`, `GRAFANA_API_KEY` and
`GRAFANA_USERNAME`.

### `GRAFANA_SESSION_FILE is only supported with the stdio transport`

Remove `-t sse` or `-t streamable-http` from the arguments.

### `GRAFANA_URL (…) does not match the URL of the session in …`

Either remove `GRAFANA_URL` from the config (it is read from the session), or
sign in again with the right `--url`.

### `no Chrome, Chromium or Edge found; pass --browser or use --paste`

Pass the browser's path, for example
`--browser "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"`,
or use `--paste`.

### `preparing browser profile (is another login still running?)`

Another `mcp-grafana login`, or a Chrome window started by it, is still using
the profile. Close it and try again.

### `no Grafana session found in the browser`

You pressed Enter, or quit the browser, before Grafana finished loading after
sign-in. Run the command again and wait until you see Grafana itself.

### `grafana returned 401 Unauthorized for /api/user`

In browser mode the message starts with `captured session was rejected`: sign-in
didn't complete. In paste mode it ends with a note about rotation: the browser
had already rotated the cookie you copied. Copy a fresh one from a private
window and close the window right after.

### `updating Cursor secret GRAFANA_SESSION_FILE: unexpected status …`

- `400 Bad Request`: the session JSON is probably larger than 4,096 bytes.
- `401 Unauthorized`: `CURSOR_API_KEY` is wrong or has been revoked.
- `403 Forbidden`: the key is a service account key, or your team has disabled
  personal secrets. Use a personal API key, or ask a Cursor admin.

### `CURSOR_API_KEY must be set to save the rotated Grafana session`

Add your Cursor API key as the `CURSOR_API_KEY` personal secret.

## Reference

### Environment variables

| Variable               | Description                                                                                                                                           |
| :--------------------- | :---------------------------------------------------------------------------------------------------------------------------------------------------- |
| `GRAFANA_SESSION_FILE` | Path to the session file. With `CURSOR_CLOUD=true`, the session JSON itself. Turns on session auth.                                                       |
| `CURSOR_CLOUD`         | `true` turns on Cursor cloud mode (case-insensitive).                                                                                                    |
| `CURSOR_API_KEY`       | Cloud mode only. Your personal Cursor API key, used to save rotated sessions.                                                                            |
| `GRAFANA_URL`          | Optional with session auth. Must match the session's URL if set.                                                                                       |

### Files

| Path                                          | Purpose                                                   |
| :-------------------------------------------- | :-------------------------------------------------------- |
| `~/.config/mcp-grafana/session.json`          | Default session file (`0600`).                            |
| `~/.config/mcp-grafana/session.json.lock`     | Lock file that serializes rotation between processes.     |
| `~/.config/mcp-grafana/browser-profile/`      | Chrome profile used by browser-mode sign-in. It lives next to the session file, so a `--session-file` in another directory gets its own profile. |

### Platform notes

- **macOS and Linux:** full support, including sharing one session file between
  several processes.
- **Linux with snap Chromium** (the default `chromium` on Ubuntu): snap
  confinement may block the profile directory under `~/.config`. If browser
  sign-in fails, install Google Chrome from its `.deb` package or use `--paste`.
- **Windows:** not tested and no release binaries. Session auth should work, but
  there is no lock between processes, so use one session file per MCP server.
  To finish browser sign-in, quit the browser rather than pressing Enter.

### Code

| File                                                                 | Contents                                         |
| :------------------------------------------------------------------- | :----------------------------------------------- |
| [`cmd/mcp-grafana/login.go`](../cmd/mcp-grafana/login.go)             | `login` command and server start-up wiring.       |
| [`session_auth.go`](../session_auth.go)                              | Session store, rotation and HTTP round tripper.   |
| [`session_cursor.go`](../session_cursor.go)                          | Writing rotated sessions to Cursor.               |
| [`session_lock_unix.go`](../session_lock_unix.go)                    | Lock file between processes (not on Windows).     |
