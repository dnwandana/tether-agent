# Security

This page tells how Tether Agent controls what the agent can do, and where the limits are. Read it
before you give the agent access to a machine with important data.

> [!WARNING]
> Tether Agent has no sandbox. Commands run on the host as the user that runs Tether Agent, with
> all the permissions of that user. Do not run Tether Agent as root. Use a user with only the
> permissions that the agent needs.

The [security model](https://github.com/dnwandana/tether-agent/blob/main/docs/SECURITY.md) in the
repository gives the full rule lists.

## The layers

| Layer | What it protects |
|---|---|
| [The approval gate](#the-approval-gate) | Asks you before a dangerous shell command runs. |
| [Protected paths](#protected-paths) | Keeps the agent away from its own config, secrets, and database. |
| [Secrets](#secrets) | Keeps API keys out of child processes, logs, and the model input. |
| [Provider keys](#provider-keys) | Sends each API key only to its own host, and keeps it out of errors, exports, and backups. |
| [Toolsets](Tools#turn-toolsets-on-or-off) | Hides tools from a channel. |
| [Telegram access](#telegram-access) | Lets only approved users talk to the bot. |
| [The skill guard](#the-skill-guard) | Scans skills for dangerous content. |

## The approval gate

The approval gate checks each `terminal` call. It also checks the full text of each `execute_code`
script before the script runs. The gate is a port of the Hermes Agent approval flow.

### The order of the checks

1. **Normalize.** The gate applies Unicode NFKC, removes ANSI escape sequences and zero-width
   characters, and changes the command to lower case. Hidden characters then cannot get past the
   rules.
2. **Hardline rules.** These commands are always blocked. No mode and no answer permits them:
   - a recursive delete of `/`, of a system folder, or of the home folder
   - `mkfs`
   - `dd` or a redirection to a raw block device
   - a fork bomb
   - `kill -1`
   - shutdown and reboot commands
   - `tether skills install`, `tether skills update`, and `tether skills uninstall`
3. **Sudo guard.** The gate blocks `sudo -S` when `SUDO_PASSWORD` is not set in `.env` or in
   the process environment. Without a password, the model guesses it.
4. **Gateway self-stop.** In the gateway only, the gate blocks a command that stops or restarts
   the gateway itself. Such a command can never finish, because the stop waits for the running
   turn. The agent tells you to send `/restart` instead. A run that cron started does not get
   this block.
5. **Dangerous rules.** A command that matches one of the 56 dangerous rules needs an approval.
   Examples are `rm -r`, `chmod 777`, `curl … | sh`, `git push --force`, `git reset --hard`, and
   SQL `DROP`. A shell write to a protected path is also dangerous.
6. **No match.** The command runs.

### Approval modes

`approvals.mode` sets the result for a dangerous command:

| Mode | Result |
|---|---|
| `manual` (default) | The gate asks you. |
| `smart` | The default fallback chain, which starts at the primary provider, judges the command first. It has 30 seconds. On `APPROVE`, the command runs, and the rule is permitted for the rest of the session. On `ESCALATE` or on an error, the gate asks you. |
| `off` | The command runs with no question. The hardline rules and the sudo guard still apply. In the gateway, the block on a stop or a restart of the gateway also still applies. |

### Cron runs

No user is present in a cron run. `approvals.cron_mode` sets the result for a dangerous command in
a cron run:

- `deny` (default): the gate refuses the command.
- `approve`: the command runs.

Two cases come first. When `approvals.mode` is `off`, the command runs. When you gave the `always`
answer for the rule, the command runs. A child agent of a cron job follows the same rules.

### Your answer

The gate waits `approvals.timeout_s` seconds for your answer. The default is 600 seconds. No answer
is a denial.

| Answer | Effect |
|---|---|
| `once` | Permits this command only. |
| `session` | Permits the same rule for the rest of the session. |
| `always` | Permits the same rule from now on. The program saves it in `state.db`. |
| `deny` | Refuses the command. The model gets `BLOCKED by user: <pattern name>` and an instruction to stop and wait for you. |

### What you see in the CLI

```
⚠ Approval needed
  <summary>
  Flagged: <pattern name>
  <the command>
[o]nce / [s]ession / [a]lways / [d]eny:
```

The summary is the `description` that the agent gives with the command. With no `description`,
the CLI shows the pattern name and one line of the command.

When a child agent asks, the question also shows `(from subagent N)`. Ctrl-D is a denial.

The command shows each control or format character as a mark, for example `<U+202E>`. A line
break and a tab stay. Such a character can hide a part of the command or change its order on the
screen.

### What you see in Telegram

The bot sends a message with the title **Approval needed**, a short summary when the agent gives
one, the reason for the flag, and the command. The message has four buttons:

- ✅ Allow once
- ✅ Allow in this session
- ✅ Always allow
- ❌ Deny

After your answer or after the timeout, the bot edits the message. It shows the result, for
example "✅ **Approval allowed** · this session", the summary, and the command in a closed quote.
Tap the quote to see the full command. Like the CLI, the message shows each control or format
character of the command as a mark. A link in the message gets no preview.

You can also answer with a typed command:

- `/approve` permits the oldest pending command once.
- `/approve session` or `/approve always` gives a wider scope.
- `/deny` refuses the oldest pending command.
- Add `all` to answer all pending commands, for example `/approve all` or `/deny all`.

These commands also work while a turn waits for the answer.

## Protected paths

The file tools (`write_file`, `patch`, and `skill_manage`) refuse to write to these paths. The
approval gate treats a shell write to them as dangerous.

- `~/.ssh/`
- `~/.tether/.env`
- `~/.tether/config.yaml`
- `~/.tether/state.db`, with its `-wal` and `-shm` files, and `state.db.lock`
- `~/.tether/skills/` (only `skill_manage` can write there)
- `~/.tether/scripts/` (only you put cron scripts there)
- `~/.tether/backups/`

The file tools resolve `..` and symbolic links before the check. A link that points to a protected
path is refused.

The shell check reads the text of the command only. It does not resolve a relative path or a
symbolic link.

## Secrets

### The .env file

- Keep your secrets in `~/.tether/.env`. The config refers to them with `${NAME}`.
- Set the file mode to 0600. The program does not start when the group or other users have any
  permission on the file.
- A variable in the process environment overrides the same name in `.env`.

### Child processes get no secrets

These child processes get an environment with no secrets:

- `terminal`
- `execute_code`
- cron scripts
- MCP stdio servers
- media commands
- goal gates

A name is a secret when one of these is true:

- `.env` has the name.
- The name ends in `_KEY`, `_TOKEN`, or `_SECRET`.
- The name contains `PASSWORD`.

These names pass through to the child:

- the names in `terminal.env_passthrough`
- the `required_environment_variables` of a skill that the session loaded
- the `env` block of an MCP server, for that server only

### Redaction

The redactor masks API keys, tokens, and credentials in the logs, in tool output, in error text,
and in the text that goes to the model.

- A token of fewer than 18 characters becomes `***`.
- A longer token keeps its first 6 and its last 4 characters.
- The redactor also masks the exact values from `.env` and the values of the secret names in the
  process environment. It ignores values of fewer than 8 characters. This rule also covers each
  provider key.

### Backups

`tether backup` writes one archive with the mode 0600. These rules keep the secrets out of it:

- The archive never contains `.env` or `.history`.
- A literal key in `providers.<name>.api_key`, `telegram.bot_token`, or an MCP `env` or
  `headers` block becomes a `${NAME}` reference.
- The backup masks each known secret value in the messages, the stored reasoning, the memory
  files, the skills, and the scripts.
- `tether restore` lists the variables that `.env` must contain. The list has each `${NAME}` of
  `config.yaml`, and the key env of each preset entry with no `api_key`.

Refer to [Running as a Service](Running-as-a-Service#back-up).

## Provider keys

The setup wizard writes each API key to `.env`. The config refers to the key with `${NAME}`, or
it uses the key env of the preset. When a second entry of the same preset gets a different key,
the wizard writes that key as `<NAME>_2`. Refer to [Providers](Providers#api-keys).

These rules protect the keys of the provider entries:

- **A key goes only to its own host.** A preset entry reads the key env of its preset only when
  its `base_url` is the URL of the preset. An entry that moves a preset to another host needs its
  own `api_key`. The quirks of a preset, for example extra headers, also stay on the URL of the
  preset.
- **Errors carry no key.** The adapter removes the API key from the error text and from the
  error body. It keeps only the first 512 bytes of the body.
- **Chat replies carry no detail.** The fallback state in `/new` and `/model` shows only a short
  cause: `HTTP <code>`, `no connection`, or `error`. It never shows the response body or the URL.
- **An entry that is not complete makes no call.** A preset entry with no API key and an entry
  with no `model` are not complete. When `primary` is not complete, the load stops. Another
  entry loads, but its calls fail at once, with no network request. The log names the entry
  and the cause.
- **A custom entry can have no key.** An entry with no preset and no API key is complete. Its
  calls go with no auth header, for example to a local server.
- **The Responses API keeps no copy.** The `responses` adapter sends `store: false`. The
  provider keeps no copy of the conversation.
- **Exports leave out the reasoning.** `tether sessions export` writes no stored reasoning, also
  no encrypted reasoning.

The `openrouter` preset sends the headers `HTTP-Referer` and `X-Title` with the name of Tether
Agent. OpenRouter shows this name on its activity page. The headers hold no data about you.

## Telegram access

The bot answers only approved users. You approve a user with a pairing code or with an allow list
in `config.yaml`. Pairing codes are short-lived, and `state.db` keeps only a salted hash of each
code. Refer to [Telegram](Telegram#pairing) for the steps.

When other people can talk to the bot, set `TETHER_MEDIA_DELIVERY_STRICT=1` in `.env`. In the
default mode, the bot can send any local file that is not protected and not in a denied folder.
A prompt injection can make the model name any file.

## The skill guard

The skill guard scans a skill before the agent uses it. It has 121 threat rules in 12 groups. A
`tether skills install` always scans the skill in strict mode. The agent can never install a skill
itself. Refer to [Skills](Skills#the-skill-guard).

## Known limits

- **No sandbox.** Commands run on the host, as the Tether Agent user.
- **MCP tools are not checked.** The approval gate cannot see what an MCP server runs. Enable only
  the servers that you trust.
- **The browser has no check for private addresses.** `web.block_private_networks` applies to the
  web tools only. A browser page can open a local address, for example the page of a router.
- **One browser profile.** All sessions share the cookies and the logins.
- **Scripts can build commands.** The gate checks the text of an `execute_code` script. It cannot
  see a command that the script builds while it runs.
- **The fallback chain sends the conversation to other providers.** When an entry fails before
  the first byte, the next entry gets the full request. Put only the providers that you trust in
  `agent.fallback`.
- **Cron scripts run with no approval.** Only files in `~/.tether/scripts/` can run as cron
  scripts, and the agent cannot write there without an approval.
- **Goal gates run with no approval prompt.** You write each gate with `/goal gate add`. The
  gate is checked once when you add it.

## Recommended settings for a VPS

1. Create a separate user for Tether Agent. Do not use root.
2. Set the mode of `~/.tether/.env` to 0600.
3. Keep `approvals.mode` at `manual` or `smart`.
4. Keep `approvals.cron_mode` at `deny`.
5. Keep `web.block_private_networks` at `true`.
6. Hide the `browser` and `code_execution` toolsets from a channel that you do not fully trust.
7. Set `telegram.allowed_users` to your own user id only.
8. Set `TETHER_MEDIA_DELIVERY_STRICT=1` in `.env` when other users can talk to the bot.
9. Enable only the MCP servers that you trust.

```yaml
approvals:
  mode: smart
  cron_mode: deny
  timeout_s: 600
web:
  block_private_networks: true
toolsets:
  telegram: [terminal, file, web, memory, skills, ask, cron]
```
