# Agent setup: Grafana MCP with browser-session login

**For people:** you don't need to read the rest of this page. Paste this into
Claude Code, Cursor, Codex or another coding agent:

> Set up the Grafana MCP server from https://github.com/pkajaba/mcp-grafana for me by
> following `docs/agent-setup.md` in that repository. My Grafana URL is
> `https://<your-instance>`.

The agent installs the server and configures your MCP client. You only sign in
when a browser window opens. Background and details are in
[browser-session-login.md](browser-session-login.md).

---

**For agents:** follow these steps in order. Each step says how to check it
worked. If a check fails, stop, tell the user what failed, and point them to
[Troubleshooting](browser-session-login.md#troubleshooting).

## Rules

1. **Never reveal the session.** Don't `cat`, print, log, paste, upload or
   commit the session file or any cookie value, and don't echo it back to the
   user. To check it, use `test -s`, `wc -c` and file permissions only.
2. **The user signs in, not you.** You can't complete SSO or MFA. Never type,
   ask for or store the user's password.
3. **Don't guess the Grafana URL.** If the user didn't give one, ask for it.
4. **Merge, don't overwrite, MCP configs.** Back up a config file before you
   change it. If a server named `grafana` already exists, ask the user whether to
   replace it.
5. **Don't add other Grafana credentials** (`GRAFANA_SERVICE_ACCOUNT_TOKEN`,
   `GRAFANA_SERVICE_ACCOUNT_TOKEN_FILE`, `GRAFANA_API_KEY`, `GRAFANA_USERNAME`) to
   the same server entry. The server refuses to start with both.
6. **Use absolute paths in MCP configs.** Most clients don't expand `~` or
   `$HOME`.

The steps assume macOS or Linux and the default paths below. On Windows, adapt
the paths. Note that on Windows several processes must not share one session
file.

| What         | Path                                   |
| :----------- | :------------------------------------- |
| Source       | `$HOME/src/mcp-grafana`                |
| Binary       | `$HOME/bin/mcp-grafana`                |
| Session file | `$HOME/.config/mcp-grafana/session.json` |

## 1. Check prerequisites

```bash
go version
git --version
```

- Go must be installed. After cloning (step 2), compare `go version` with the
  `go` line in `go.mod`. A newer Go is fine. If yours is older, Go usually
  downloads the right toolchain by itself during the build.
- Check for a supported browser for sign-in:

  ```bash
  # macOS
  ls -d "/Applications/Google Chrome.app" "/Applications/Chromium.app" \
        "/Applications/Microsoft Edge.app" "/Applications/Brave Browser.app" 2>/dev/null
  # Linux
  command -v google-chrome google-chrome-stable chromium chromium-browser microsoft-edge
  ```

  If none is found, use paste mode in step 3.

## 2. Get the code and build

If the current directory is already a checkout of this repository, build from
it and skip the clone.

```bash
REPO_DIR="$HOME/src/mcp-grafana"
if [ -d "$REPO_DIR/.git" ]; then
  git -C "$REPO_DIR" pull --ff-only
else
  git clone https://github.com/pkajaba/mcp-grafana.git "$REPO_DIR"
fi
mkdir -p "$HOME/bin"
(cd "$REPO_DIR" && go build -o "$HOME/bin/mcp-grafana" ./cmd/mcp-grafana)
```

**Check:**

```bash
"$HOME/bin/mcp-grafana" login --help 2>&1 | head -1
```

The output must be `Usage: mcp-grafana login [flags]`. If it isn't, you built
upstream `mcp-grafana`, not this fork.

If `git clone` fails, stop and show the user the error.

## 3. Sign in (the user does this)

### Browser mode

Start the login **in the background**, because it waits until the user has
finished signing in:

```bash
"$HOME/bin/mcp-grafana" login --url "<GRAFANA_URL>" --timeout 10m
```

Then tell the user, in these words or similar:

> A Chrome window is opening. Sign in to Grafana there. Once you can see
> Grafana, **quit that browser window** (Cmd+Q on macOS). Closing the tab isn't
> enough.

Quitting the browser is what finishes the command, because your shell can't
pass the user's Enter key to it.

**Check:** the command exits with status 0 and prints `Signed in to <url> as <user>`.
Then:

```bash
test -s "$HOME/.config/mcp-grafana/session.json" && echo "session saved"
# Permissions must be 600:
stat -f '%Lp' "$HOME/.config/mcp-grafana/session.json" 2>/dev/null \
  || stat -c '%a' "$HOME/.config/mcp-grafana/session.json"
```

### Paste mode

Use this when no supported browser was found, or browser mode failed. Paste
mode needs the user to paste a cookie, so **the user must run it in their own
terminal**. Give them this command:

```bash
~/bin/mcp-grafana login --url <GRAFANA_URL> --paste
```

Wait until they confirm it printed `Signed in to …`, then run the checks above.

## 4. Configure the MCP client

Work out the absolute home directory first (`echo "$HOME"`) and use it in place
of `/Users/you` below.

### Claude Code

```bash
claude mcp add --scope user \
  --env GRAFANA_SESSION_FILE="$HOME/.config/mcp-grafana/session.json" \
  grafana -- "$HOME/bin/mcp-grafana"
```

**Check:** `claude mcp list` shows `grafana`.

### Codex

```bash
codex mcp add grafana \
  --env GRAFANA_SESSION_FILE="$HOME/.config/mcp-grafana/session.json" \
  -- "$HOME/bin/mcp-grafana"
```

Or merge this into `~/.codex/config.toml` (note the underscore in `mcp_servers`):

```toml
[mcp_servers.grafana]
command = "/Users/you/bin/mcp-grafana"
args = []
env = { GRAFANA_SESSION_FILE = "/Users/you/.config/mcp-grafana/session.json" }
```

**Check:** `codex mcp get grafana` shows the entry.

### Cursor (local)

Merge this entry into `~/.cursor/mcp.json` under `mcpServers`. Create the file
if it doesn't exist.

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

### Claude Desktop

Merge the same entry into:

- macOS: `~/Library/Application Support/Claude/claude_desktop_config.json`
- Windows: `%APPDATA%\Claude\claude_desktop_config.json`

**Check, for JSON configs:** the file is still valid JSON, for example
`python3 -m json.tool <file> > /dev/null`, and the other servers are still there.

### Optional: read-only

If the user wants the agent to only read from Grafana, add
`"args": ["--disable-write"]` to a JSON entry, or `args = ["--disable-write"]` in
Codex's TOML. With the `claude mcp add` or `codex mcp add` commands, end with
`-- "$HOME/bin/mcp-grafana" --disable-write`.

## 5. Verify

MCP servers load when the client starts. Ask the user to restart the client:
start a new Claude Code or Codex session, reload the Cursor window, or quit and
reopen Claude Desktop.

Then call the **`user_info`** tool of the `grafana` server. It must return the
user's own Grafana login. Tell the user who they are signed in as and which
Grafana instance it is. You're done.

If the tool fails, check the error against
[Troubleshooting](browser-session-login.md#troubleshooting).

## 6. Optional: Cursor cloud agents

Do this only if the user asks for cloud agents. Most of it happens in the Cursor
dashboard, so guide the user through it rather than doing it yourself. Cursor's
own docs: https://cursor.com/docs/cloud-agent.

1. **Check the size.** The session must fit in a Cursor secret (4,096 bytes).

   ```bash
   wc -c < "$HOME/.config/mcp-grafana/session.json"
   ```

   If it's larger, the user should sign in again with `--paste` and copy only
   the Grafana cookies.

2. **Copy without printing.** Offer to put the session on the user's clipboard
   so it never appears in the chat or terminal:

   ```bash
   pbcopy < "$HOME/.config/mcp-grafana/session.json"   # macOS
   ```

3. **Add personal secrets** (user, in the Cursor dashboard):

   - `GRAFANA_SESSION_FILE`: paste the clipboard contents.
   - `CURSOR_API_KEY`: a Cursor API key that belongs to the user, not a service
     account key.

   Make sure no **environment** secret is also called `GRAFANA_SESSION_FILE`,
   because it would override the personal one.

4. **Install the binary in the cloud environment.** Add the build from step 2 to
   the environment's setup, which can be agent-led setup, a snapshot, or a
   Dockerfile, as described in Cursor's docs. The environment needs read access
   to this repository.

5. **Add the MCP server** (user, through the MCP dropdown in cursor.com/agents):
   command `mcp-grafana` (or its absolute path in the environment), environment
   variable `CURSOR_CLOUD=true`. Don't set `GRAFANA_SESSION_FILE` there. It comes
   from the secret.

6. **Verify.** Secrets reach an agent when it starts, so start a **new** cloud
   agent and have it call `user_info`.

Tell the user: run **one cloud agent at a time** on this session. Agents that
run at the same time can log each other out (see
[Cursor cloud agents](browser-session-login.md#cursor-cloud-agents)).

## Uninstall

```bash
claude mcp remove grafana --scope user    # Claude Code
codex mcp remove grafana                  # Codex
rm -f "$HOME/bin/mcp-grafana"
rm -rf "$HOME/.config/mcp-grafana"        # session, lock file and browser profile
```

Remove the `grafana` entry from any JSON configs you changed. Tell the user to
also revoke the session in Grafana under **Profile → Sessions**, and to delete
the Cursor secrets if they added any.
