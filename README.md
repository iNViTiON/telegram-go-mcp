# telegram-go-mcp

A small, auditable [MCP](https://modelcontextprotocol.io) server that lets an AI assistant **read your Telegram** and **save drafts**, and nothing else. You review and send every message yourself, in the official Telegram app.

It replaces a third-party Python server that returned bare message text with no date or sender and could not reach forum topics.

## What it can and cannot do

| | |
|---|---|
| Read chats, with ids, dates, senders, unread counts | yes (`list_chats`) |
| List a forum group's topics | yes (`list_topics`) |
| Read messages, reply threads, search one chat | yes (`read_messages`, `read_thread`, `search_messages`) |
| Save a draft for you to send | yes (`save_draft`), see the caveat under Status |
| Send, edit, delete, forward, mark as read, react, join, leave | **no, structurally** |

"Structurally" means the program contains an allowlist of Telegram requests and refuses everything else before it reaches the network. You can verify that by reading one file. See [Auditing the safety claim](#auditing-the-safety-claim).

## Status

Working and tested: `whoami`, `list_chats`, `list_topics`, `save_draft`, sign-in, the guard, and sharing one Telegram session between the several copies the desktop app starts.

Message reading (`read_messages`, `read_thread`, `search_messages`) is tested offline only and has **not** been run against a real account yet.

`save_draft` has been checked end to end against a real account: a draft saved to Saved Messages and one into a forum topic both appeared in the official app, and the topic draft is stored exactly as the app stores a hand-typed one (no message replied to, `top_msg_id` set to the topic), so it shows no reply bar. Replacing by compare-and-swap, overwriting and clearing were each run live, and refusals left the existing draft untouched. **Not yet checked live:** a draft in a forum's General topic, and a draft you are still typing on another device (the apps sync those after a delay).

Linux only. The sign-in prompt and the instance lock use Linux-specific code, and the project does not build on macOS or Windows.

## Install

You need Go 1.25.0 or newer (`go.mod` sets the floor). The binaries are fully static: no libc, no runtime dependencies, nothing resolved at start-up.

```bash
git clone <this repo> ~/Documents/telegram-go-mcp
cd ~/Documents/telegram-go-mcp
CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/ ./cmd/telegram-mcp ./cmd/telegram-mcp-login
file bin/telegram-mcp    # should say: statically linked
```

### NixOS

The steps above were run on NixOS with Go from the system profile, and the result is `statically linked`. If Go is not in your profile, this should give you a toolchain for one command (not tested here):

```bash
nix shell nixpkgs#go -c env CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/ ./cmd/telegram-mcp ./cmd/telegram-mcp-login
```

Because the binary is static you can build it anywhere and point your config at it. There is **no Nix package or flake in this repo yet**. A `buildGoModule` derivation would need a `vendorHash` computed from `go.sum`; that is a sensible follow-up if you want it declarative in your NixOS config.

Dependencies are pinned: `gotd/td` and the MCP Go SDK at exact versions in `go.mod`, with hashes in `go.sum`. An unpinned dependency range is what broke the Python server this replaces.

## Set up

### 1. Get an API id and hash

Log in at <https://my.telegram.org>, open *API development tools*, and create an app. You get an `api_id` (a number) and an `api_hash`.

### 2. Put them in a private file

Create the file as private **before** it holds anything, then edit it. Do not use `echo`: that leaves the hash in your shell history.

```bash
install -D -m 600 /dev/null ~/.config/telegram-go-mcp/credentials
${EDITOR:-nano} ~/.config/telegram-go-mcp/credentials
```

Two lines:

```
TELEGRAM_API_ID=12345678
TELEGRAM_API_HASH=0123456789abcdef0123456789abcdef
```

The program refuses to start if this file, the state directory or the session file can be read by anyone but you, and it rejects `<placeholder>` values. Nothing secret goes in `claude_desktop_config.json`; the desktop app rewrites that file.

Paths follow XDG: `$XDG_CONFIG_HOME` (default `~/.config`) and `$XDG_STATE_HOME` (default `~/.local/state`).

### 3. Sign in once

Run this in a terminal yourself. Phone, login code and two-step password are typed on `/dev/tty`, never on the command line or in the environment.

```bash
./bin/telegram-mcp-login
```

It echoes the login code (short-lived, and a typo triggers another code request) and hides only the two-step password. It will not accept Telegram's terms of service or create an account on your behalf.

Telegram shows a new-login alert and lists a session named `telegram-go-mcp` under *Settings → Devices*. Ending that session there signs the server out; that is your kill switch.

### 4. Check it (optional)

Connects with the server's own read-only policy, prints who you are, and logs every request that passes the guard while idle:

```bash
./bin/telegram-mcp-login probe -for 1m
```

Expect `RPCs through the guard while idle: 0`. Add `TELEGRAM_MCP_AUDIT=1` to any run to log every request type to stderr.

### 5. Add it to Claude Desktop

Edit `~/.config/Claude/claude_desktop_config.json` and add one entry. Use the absolute path and **no `env` block**:

```json
{
  "mcpServers": {
    "telegram": {
      "command": "/home/you/Documents/telegram-go-mcp/bin/telegram-mcp"
    }
  }
}
```

Merge into the existing `mcpServers` object rather than replacing the file. With `jq`, from a backup copy:

```bash
F=~/.config/Claude/claude_desktop_config.json
cp -p "$F" "$F.bak"
jq --indent 2 '.mcpServers = ((.mcpServers // {}) + {"telegram": {"command": "'"$PWD"'/bin/telegram-mcp"}})' "$F" > "$F.new" \
  && cat "$F.new" > "$F" && rm "$F.new"
```

Then **quit Claude Desktop fully from the tray** (closing the window leaves it running) and start it again. A rebuilt binary only takes effect after another restart.

## Use

Ask in plain language: *"What chats have unread messages?"*, *"Who am I signed in as?"*.

| Tool | Arguments | Returns |
|---|---|---|
| `whoami` | none | your id, name, username (never your phone number) |
| `list_chats` | `limit` (1–100, default 30), `archived`, `cursor` | chats, newest activity first, plus `next_cursor` when more exist |
| `read_messages` | `chat_id`, `topic_id`, `limit` (1–100, default 30), `before_id` | messages newest first, each with id, RFC3339 date, sender, topic, reply target, text, attachment type and a link; `next_before_id` pages back |
| `read_thread` | `chat_id`, `message_id`, `limit`, `before_id` | the replies to one message, or the comments under a channel post |
| `search_messages` | `chat_id`, `query`, `topic_id`, `limit`, `before_id` | matches in **one** chat, optionally one topic |
| `list_topics` | `chat_id`, `limit` (1–100, default 50) | a forum group's topics with unread counts and each topic's current draft; topic 1 is General |
| `save_draft` | `chat_id`, `text`, `topic_id`, `reply_to_msg_id`, `overwrite`, `expected_draft_date`, `clear` | where the draft was saved, the stored draft read back, and any draft it would have replaced |

Each chat has a kind-prefixed `id` (`user:1`, `chat:2`, `channel:3`), `kind` (`user`, `bot`, `group`, `supergroup`, `channel`), `title`, `username`, unread counts, and its last message with an RFC3339 UTC date, sender and a snippet capped at 200 characters. `forum: true` marks supergroups that have topics.

Things worth knowing:

- Pinned chats come first and are included **in addition to** `limit`, so the first page can be longer than `limit`. Later pages exclude them.
- Pass `next_cursor` back as `cursor` to continue. It is opaque; do not edit it.
- Chat titles and message text are written by other people. The server tells the model to treat them as data, not instructions. Treat that as a mitigation, not a guarantee.

### Drafts

`save_draft` only stores text in the compose box. It never sends: the draft is visible to you, and you review and send it from the Telegram app. `chat_id` is `me` (Saved Messages), `@username`, or an id from `list_chats` such as `channel:123`. A kind-prefixed id must have been seen in `list_chats` or `list_topics` in this server's lifetime, because Telegram needs the chat's access hash.

Guards, all checked before anything is written:

- A forum group needs `topic_id`; the server never guesses a placement.
- If the chat or topic already has a draft, the call is **refused** and shows you the existing text. Replace it only with `overwrite=true`, or with `expected_draft_date` set to the date of the draft that was seen, which refuses if it changed since.
- Empty text deletes a draft, so it needs `clear=true` and the same permission to replace.
- Text over 4096 characters is refused.
- After saving, the draft is read back and returned together with where to look, e.g. `Dev Group › Frontend`. A save is reported as done **only if the read-back confirms it**. If Telegram accepts a draft but it cannot be found where it should be, the tool returns an error saying so, instead of claiming success.
- A draft in a topic is addressed the way the official app does it, found by reading drafts typed by hand: no message replied to, `top_msg_id` set to the topic. The two other shapes tried (topic id as the reply target, or the sending docs' rule) either showed a reply bar or were lost.

Caveat: the official apps push a draft you are still typing to Telegram only after a short delay, so a draft typed seconds ago may not be visible to the check yet. The compare-and-swap narrows that race but cannot close it.

### Reading messages

Every message carries its id, an RFC3339 UTC date, the sender, the forum topic it belongs to (1 is General), what it replies to, its text and attachment type. A plain message in a topic is not reported as a reply to the topic's first message. Reading does not mark anything as read.

Things worth knowing:

- Messages come **newest first**. Pass `next_before_id` back as `before_id` for older ones. The cursor follows what Telegram returned, so topic filtering cannot make paging skip messages.
- A forum topic is read as a message thread. **General** is the chat's messages that belong to no other topic, so it is read from the overall history and filtered; a page can hold fewer than `limit`. Neither has been checked against a real account yet.
- Search covers one chat, not the whole account.
- `link` is set only for messages of the channel being read. Comments on a channel post live in another chat and get none.
- Message text is written by other people. The server tells the model to treat it as data, but that is a mitigation, not a guarantee, and a hostile message is the main way text could try to steer the assistant. The assistant still cannot send anything.

## One process owns the session

Telegram can invalidate a login key that two live connections use at once, which forces a fresh sign-in. Claude Desktop starts more than one copy of an MCP server (one for chat, one shared by Cowork and Code), so the copies cooperate:

- The first copy takes a lock (`$XDG_STATE_HOME/telegram-go-mcp/lock`) and becomes the **owner**. It is the only process that connects to Telegram. It also listens on a unix socket (`.../sock`, mode 0600 in the 0700 state directory, and it checks each peer's uid).
- Every later copy is a **shim**. It builds no Telegram client. It copies its stdin and stdout to the owner's socket, where it becomes a full MCP session on the same server and the same guard. Every context gets working tools.
- If the owner's own client quits, the owner keeps serving the others and exits 30 seconds after the last one disconnects.
- `telegram-mcp-login` takes the same lock, so sign-in and `probe` refuse while the app is running. Quit it first.

Limitation: if the owner process dies, shims connected to it exit with an error ("went away") and the app has to start them again; they do not take over. If a shim cannot reach an owner within 5 seconds it still answers MCP, but its tools report that another instance holds the session.

The session file is written atomically, so several processes cannot leave a half-written file.

## Files

| Path | Purpose | Mode |
|---|---|---|
| `~/.config/telegram-go-mcp/credentials` | API id and hash | 0600, enforced |
| `~/.local/state/telegram-go-mcp/` | state directory | 0700, enforced |
| `~/.local/state/telegram-go-mcp/session.json` | Telegram login. **Treat like a password.** | 0600, enforced |
| `~/.local/state/telegram-go-mcp/lock` | single-instance lock | 0600 |
| `~/.local/state/telegram-go-mcp/sock` | the owner serves other copies here | 0600 |

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| `credentials file not found` | Create it as in step 2. |
| `file permissions too open ... run: chmod 600 ...` | Do what it says. Group or world access is refused on purpose. |
| `placeholder` error | The file still has `<id>` or `<hash>`. |
| Tool says *not signed in or was revoked* | Run `./bin/telegram-mcp-login` again (quit the desktop app first). |
| Tool says *another instance holds the session ... could not reach it* | This copy is a shim that found no working owner (the owner died, or its socket could not be created). Quit Claude Desktop fully from the tray and start it again. |
| Shim exits with *went away* | The owner process died. The app should start the server again; if not, restart the app. |
| Tool says *connection is not ready* | Retry in a few seconds; the first call after start-up waits up to 15 s for the connection. |
| Server is not listed in the app | Check the app's MCP log for the `telegram` server. The first line names the credentials and session paths it resolved; if they differ from what you expect, the app's environment differs from your shell. |
| The `telegram` entry vanished from the config | The app rewrote the file from memory. Add it again. |

## Auditing the safety claim

The whole policy is [`internal/guard/guard.go`](internal/guard/guard.go) (about 150 lines). Every Telegram request passes `Allowed()`, and the default is deny. The server's policy allows 14 reads and one write, `messages.saveDraft`, which is text-only (no media, effect or suggested post). The sign-in policy allows only the login requests. There is no allowlisted request that sends, edits, deletes, forwards, or marks anything read.

One guarded client serves every session, including relayed ones, so the shim path cannot widen what is allowed: a shim is a byte bridge that knows nothing about MCP. The single place a Telegram client is built is [`internal/tgclient/client.go`](internal/tgclient/client.go), with the guard first in the middleware chain.

```bash
go test ./...
```

What the tests prove:

- `messages.sendMessage` is rejected **through the real client**, immediately, not by timing out.
- Hand-built `invokeWithLayer` and `initConnection` wrappers are rejected.
- Every request type in gotd's schema (868 at the pinned version) is checked against a second, literal list in the test file, so widening the policy means editing two places on purpose.
- Allowed request names contain no write-looking verbs, and the wire ids of the dangerous requests are pinned.
- An AST test fails if a second Telegram client, a raw invoker, or a call to the sign-in code appears in the server binary.
- Each tool is run behind the real guard and its Telegram requests are pinned: `whoami` makes only `users.getUsers`, `list_chats` only `messages.getDialogs`.
- A test runs the real `main()` and fails on any non-JSON byte on stdout.

Honest limits: the guard sits in gotd's middleware chain, so it covers every request this program makes through that client. It does not defend against a modified binary or modified source. The remaining third-party code is `gotd/td` (Telegram's protocol), the official MCP Go SDK, and a handful of their transitive modules, all hash-locked in `go.sum`.

## Repository layout

```
cmd/telegram-mcp/         the MCP server (what the desktop app launches)
cmd/telegram-mcp-login/   sign-in and probe, run by you in a terminal
internal/guard/           the allowlist
internal/tgclient/        the only Telegram client constructor, atomic session storage
internal/instancelock/    one process owns the session
internal/relay/           the owner serves other copies; the shim bridge
internal/config/          credentials, XDG paths, permission checks
internal/tools/           MCP tools
internal/peercache/       access hashes learned from list_chats / list_topics
internal/model/           JSON shapes returned to the model
internal/tgerrs/          Telegram errors rewritten for the model
```
