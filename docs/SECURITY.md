# Security model

This document tells how Tether Agent controls what the agent can do. It also gives the known
limits. Read the limits before you give the agent access to a machine that holds important data.

[CONFIG.md](CONFIG.md) gives the default of each key that this document names.

## Summary of the limits

- Commands run on the host, as the user that runs Tether Agent. There is no container and no
  sandbox.
- The approval gate checks shell commands only. It does not check the tools of MCP servers.
- The approval gate checks the full text of each `execute_code` script before the script runs. It
  finds a dangerous command that is written in the script as literal text. It cannot find a
  command that the script builds while it runs.
- A cron script job runs with no approval. Only files in `~/.tether/scripts/` can run as
  cron scripts. The file tools cannot write in that folder, and a shell write to it needs an
  approval.
- In the default mode, the bot can send any local file that is not protected and not in a denied
  folder (see "Files that the bot sends"). Use strict mode for a bot that other people use.
- The browser tools have no check for private addresses. All sessions share one Chrome profile.

## Approval gate for commands

The approval gate checks every `terminal` call. It also checks the full text of each
`execute_code` script, and each tool call of the script goes through the same checks as a model
call. The gate is a port of the Hermes approval flow.

### The order of the checks

1. **Normalize.** The gate applies Unicode NFKC, removes ANSI escape sequences and zero-width
   characters, and changes the command to lower case. Hidden characters then cannot get past
   the rules.
2. **Hardline list.** These commands are always blocked. No approval mode, cron setting, or
   "Always" answer can permit them:
   - a recursive delete of `/`, of a system folder, or of the home folder
   - `mkfs`
   - `dd` to a raw block device, and a redirection to a raw block device
   - a fork bomb
   - `kill -1`
   - shutdown and reboot, through `shutdown`, `reboot`, `halt`, `poweroff`, `init`, `telinit`,
     or `systemctl`
3. **Sudo guard.** The gate blocks `sudo -S` when no sudo password is set, because the model
   then guesses the password. Set `SUDO_PASSWORD` in `.env` if the agent must use sudo.
4. **Dangerous rules.** A command that matches one of the 56 Hermes dangerous rules needs an
   approval. Examples are `rm -r`, `chmod 777`, `curl … | sh`, and `git push --force`. A shell
   write to a protected path is also dangerous.
5. **No match.** The command runs.

### Approval modes

`approvals.mode` sets the result for a dangerous command:

- `manual` (the default): the gate asks the user.
- `smart`: a second call to the primary provider judges the risk. The gate permits a
  low-risk command with no question. For other commands, the gate asks the user. When the
  judge call fails, the gate also asks the user.
- `off`: the command runs with no question. The hardline list and the sudo guard still apply.

### Cron runs

No user is present in a cron run. `approvals.cron_mode` sets the result for a dangerous command
in a cron run:

- `deny` (the default): the gate refuses the command with no question.
- `approve`: the command runs.

Two cases apply before `approvals.cron_mode`:

- When `approvals.mode` is `off`, the command runs.
- When the user gave the `always` answer for the rule, the command runs.

A child run of a cron job (`delegate_task`) also has no user. It follows
`approvals.cron_mode` like the cron run. A cron run and its children cannot start a
background delegation, because the job ends before the children.

### The question and the scopes

The gate waits `approvals.timeout_s` seconds for an answer. The default is 600 seconds. No answer
is a denial.

The user answers with one of these scopes:

- `once`: permits this command only.
- `session`: permits the same rule for the rest of the session.
- `always`: permits the same rule from now on. The program saves the rule id in the
  `approvals_always` table of `state.db`. The running agent never writes its own `config.yaml`.
- `deny`: refuses the command. The model gets the result `BLOCKED by user: <description>`.

## Toolsets

Each tool belongs to one named toolset, for example `terminal`, `file`, or `web`.
`toolsets.<channel>` gives the toolsets of each channel: `cli`, `telegram`, and `cron`.

- The registry removes the hidden tools before it sends the request. The model never sees a
  hidden tool.
- A call to a hidden tool, for example from an old message, gives an error result.
- A cron job and a child run can only make the toolsets of their channel smaller.
- A cron run cannot use `cronjob` or `clarify`.

There is one exception. The background review fork gets the same tool schemas as its parent, so
the prompt cache hits. The dispatcher permits only the `memory`, `skill_view`, and
`skill_manage` tools in the fork. A call to another tool gives an error result.

## Secrets

### The .env file

- Keep secrets in `~/.tether/.env`. The config refers to them with `${VAR}`.
- Set the file mode to 0600. The program refuses to start when the group or other users have
  any permission on the file.
- A variable in the process environment overrides the same name in `.env`.

### The child environment

These child processes get an environment with no secrets:

- `terminal`
- `execute_code`
- cron scripts
- MCP stdio servers
- media commands

A name is a secret when `.env` has it, or when it ends in `_KEY`, `_TOKEN`, or `_SECRET`. A name
that contains `PASSWORD` is also a secret. The check ignores case.

These names pass through to the child:

- the names in `terminal.env_passthrough`
- the `required_environment_variables` of a skill that the session loaded
- the `env` block of an MCP server entry, for that server only

### Redaction

The redactor is a port of Hermes `agent/redact.py`. It masks API keys, tokens, and credentials in
the logs, in tool output, in error text, and in the text that goes to the model.

- A token of fewer than 18 characters becomes `***`.
- A longer token keeps its first 6 and its last 4 characters.
- The redactor also masks sensitive query parameters, for example `access_token`, `api_key`,
  `token`, and `password`.
- The redactor also masks the exact values from `.env` and the values of the secret names in
  the process environment. It ignores values of fewer than 8 characters, because they match
  too much normal text.

## Protected paths

`write_file`, `patch`, and `skill_manage` refuse to write to these paths. The approval gate
also treats a shell write to them as dangerous. `<data>` is the data folder, `~/.tether` by
default.

- `~/.ssh/`
- `<data>/.env`
- `<data>/config.yaml`
- `<data>/state.db`, and also `<data>/state.db-wal` and `<data>/state.db-shm`
- `<data>/skills/` (only `skill_manage` can write there)
- `<data>/scripts/` (only the user can put cron scripts there)

A protected file also covers each name that starts with the file name and a `-`, for example
`<data>/config.yaml-old`.

### File tools

The file tools resolve the path before the check:

- They resolve `..` and each symbolic link in the path. A link that points to a protected path
  is refused.
- A dangling link counts as its target. A write through the link cannot create the protected
  file.
- `skill_manage` refuses a write through a symbolic link in `skills/`.

### Shell commands

A shell write is a `>` or `>>` redirection, or a `tee`, `cp`, `mv`, `install`, `ln`, `rm`,
`truncate`, `chmod`, `chown`, `dd`, `rsync`, or `sed -i` command with the path as an argument.

- The rules match the absolute path. For a path in the home folder, they also match the `~`,
  `$HOME`, and `${HOME}` forms.
- The rules match by prefix. A path that starts with a protected path is also dangerous, for
  example `<data>/.envrc` or `<data>/config.yaml.bak`.
- The rules read the text of the command only. They do not resolve a relative path or a
  symbolic link.

## Network access

When `web.block_private_networks` is `true` (the default), `web_fetch`, `web_search`, and the URL
download of `view_image` refuse these addresses:

- loopback
- private ranges
- link-local
- CGNAT (`100.64.0.0/10`)
- unspecified and multicast addresses

The check runs on the IP address after DNS resolution, so DNS rebinding cannot get past it. The
client also ignores proxy settings, because a proxy hides the real target from the check. Each
redirect must use `http` or `https`.

### Browser

The browser tools use a local Chrome. They do not apply `web.block_private_networks`. This is
the Hermes behavior for a local browser.

- `browser_navigate` accepts only absolute `http` and `https` URLs. It refuses other schemes,
  for example `file:` and `javascript:`.
- `browser_navigate` accepts a private address, for example `http://127.0.0.1:8080` or the page
  of a router. A page can also redirect to a private address or load data from it.
- All sessions use one Chrome profile in `<data>/cache/chrome-profile/`. The sessions share the
  cookies and the logins. The profile stays after a restart.
- Hide the `browser` toolset from a channel when you do not trust its users.

## Telegram access

### Allowlists

The bot permits a message when the first of these checks passes:

1. `telegram.group_allowed_chats`, for a group message. `*` permits all groups.
2. `telegram.allow_all_users: true`.
3. The users that pairing approved. `state.db` keeps them.
4. `telegram.allowed_users`, and `telegram.group_allowed_users` for a group message.

When no allowlist is set and `allow_all_users` is `false`, the bot permits nobody.
`group_allowed_users` does not permit direct messages. Use `allow_all_users` only for a test bot.

### Unknown users

- In a group, the bot ignores an unknown user with no reply.
- In a direct message, `telegram.unauthorized_dm_behavior` sets the result. The default is
  `ignore` when an allowlist is set, else `pair`.

### Command buttons

- A button of a command reply works only for the user who sent the command. The callback data
  holds the Telegram user ID of that user. Another user gets `⛔ This button is for another user.`
- A tap passes the same allowlist checks as a message. A user who fails them gets
  `⛔ Only the owner can answer this prompt.`
- A tap runs the command through the same queue as a typed command, so a busy session refuses
  the same commands.
- A tap with data that is not a command gets `⚠️ This button is too old. Send the command again.`
  A tap never starts a model turn.

### Pairing codes

In pair mode, the bot replies with a pairing code. The owner approves the code on the host:

```
tether pairing approve [platform] <code>
```

The default platform is `telegram`.

- A code has 8 characters. The alphabet has no `0`, `O`, `1`, or `I`.
- `state.db` keeps a salted SHA-256 hash of each code, not the code. `pairing list` cannot show
  a code.
- A code expires after 1 hour.
- A user can request one code in 10 minutes.
- Each platform has a maximum of 3 pending codes.
- The lockout applies to the full platform, not to one user. After 5 failed approvals (a wrong
  or expired code), the platform is locked for 1 hour.
- During the lockout, `pairing approve` refuses every code, also a correct code. The bot gives
  no new codes on that platform.

`tether pairing list [platform]` shows the pending requests and the approved users.
`tether pairing revoke [platform] <user>` removes the access of a user.

### Files that the bot sends

The model can send a local file with a `MEDIA:<path>` tag or a bare absolute path in its answer.
Cron delivery uses the same rule. The rule is a port of Hermes `validate_media_delivery_path`.
The checks use the real path, after the symbolic links.

1. A protected path (see "Protected paths") is always refused.
2. A file in `<data>/cache/media/`, `<data>/cache/screenshots/`, or a folder of
   `TETHER_MEDIA_ALLOW_DIRS` is sent.
3. A file in a denied folder is refused: `/etc`, `/proc`, `/sys`, `/dev`, `/root`, `/boot`,
   `/var/log`, `/var/lib`, `/var/run`, and `~/.ssh`, `~/.aws`, `~/.gnupg`, `~/.kube`,
   `~/.docker`, `~/.config`, `~/.azure`, `~/.gcloud`, `~/Library/Keychains`.
4. In the default mode, all other files are sent.
5. In strict mode (`TETHER_MEDIA_DELIVERY_STRICT=1`), another file is sent only when it changed in
   the last 600 seconds. `TETHER_MEDIA_TRUST_RECENT_SECONDS` changes the time.
   `TETHER_MEDIA_TRUST_RECENT_FILES=0` turns this off.

A refused `MEDIA:` tag shows as `[blocked file: <path>]` in the answer. Use strict mode when users
who are not the owner can talk to the bot. A prompt injection can make the model name any file.

## Skill guard

When `skills.guard` is `true` (the default), the skill guard scans skills before the agent uses
them. The guard has all 121 threat rules of Hermes `tools/skills_guard.py`, in 12 groups. Examples
of groups are exfiltration, injection, obfuscation, and persistence.

- A critical finding gives the verdict `dangerous`. A high finding gives `caution`. Medium and
  low findings alone give `safe`.
- The guard is strict. A skill that sends a `$…KEY` variable with `curl` is `dangerous`, also
  when the skill declares the variable.
- **Agent-written skills:** the guard refuses a `dangerous` skill and returns the findings, so
  the model can write the skill again. A `caution` skill is saved, and the findings go into the
  tool result.
- **External folders:** the loader skips a `dangerous` skill and logs a warning. It loads a
  `caution` skill.
- **Bootstrap skills** are trusted. The guard does not scan them.

### API keys in skills

Do not put an API call with a key in a skill. Use this pattern:

1. Put the API call in a script under `~/.tether/scripts/`.
2. Make the skill tell the agent to run that script with `terminal`.
3. Give the key to the script through `required_environment_variables` or
   `terminal.env_passthrough`.

The guard scans skill folders only. It does not scan `scripts/`.

## Cron scripts

A cron script job runs a script with no model call and with no approval. These limits apply:

- The script must be in `~/.tether/scripts/`. A relative path resolves there. The
  program refuses a path or a symbolic link that goes outside that folder.
- `write_file` and `patch` refuse to write in `scripts/`. A shell write to `scripts/` is
  dangerous, so the approval gate asks the user. In a cron run, `approvals.cron_mode` sets
  the result.
- The script gets the child environment with no secrets.
- The script stops after 120 seconds. The program then stops the full process group.
- A failure or a timeout always sends an alert with the tail of stderr.

## MCP servers

The approval gate does not check MCP tools, because Tether Agent cannot see what an MCP server
runs. An MCP tool can do anything that its server can do.

- Enable only the MCP servers that you trust.
- Use the toolsets to hide MCP tools from a channel. Each server has the toolset
  `mcp_<server>`, and the toolset `mcp` has all MCP tools.
- A stdio server gets the child environment with no secrets, plus its own `env` block.
- The program removes credentials from MCP error text before the model sees it.
- `/reload-mcp` reads the server list again, restarts the servers, and lists their tools again.
  It adds the tools of new servers, updates changed tools, and removes the tools of disabled or
  removed servers. No restart is necessary.
- The changes apply from the next message. A turn that runs keeps its tool list until it ends.
  A call of that turn to a removed tool returns an error.
- A server that does not start during `/reload-mcp` keeps its last known tools.
- When a server changes its tool list between two reloads, the program writes the new list to
  the cache. The registry gets the new list at the next `/reload-mcp` or restart.
- When a call fails because the connection to the server is dead, the program starts the
  server again and sends the call one more time. If the server stopped in the middle of the
  first call, the call can run two times. Be careful with an MCP tool that must not run two
  times, for example a tool that sends a payment.

## Host execution

Tether Agent has no container backend in v1. Commands run on the host as the Tether Agent user,
with the permissions of that user.

- Run Tether Agent as a user with only the permissions that the agent needs.
- Do not run Tether Agent as root.
- Keep the approval mode at `manual` or `smart` on a machine with important data.

### execute_code

`execute_code` runs Python code on the host. Before the script runs, the approval gate checks the
full text of the script, the same as a shell command. A dangerous command that is in the script
as literal text, for example in a `subprocess` call, needs approval or is blocked. The gate
cannot see a command that the script builds while it runs, for example from parts of a string or
from decoded data. The tool stubs send their calls through the approval gate and the protected
path checks. Hide the `code_execution` toolset from a channel when you do not trust its users.

## Self-update

`tether update` replaces the binary with a release from GitHub.

- The command downloads the archive from the GitHub release of the repository over HTTPS.
- It checks the SHA-256 of the archive against `checksums.txt` from the same release.
- It runs the new binary one time with `version` and compares the output.
- It replaces the file with one rename in the folder of the binary.
- It never reads `config.yaml`, `.env`, or `state.db`.
- It needs write permission on the folder of the binary. A binary in `/usr/local/bin` needs
  `sudo tether update`.
- A running gateway keeps the old code until a restart. The command prints the restart command
  and does not run it.
- The checksum file comes from the same release. The check finds a cut-off or a corrupted
  download. It does not protect against a compromised GitHub account.
