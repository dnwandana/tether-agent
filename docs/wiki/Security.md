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
| [Coding agents](#coding-agents) | Asks you before a coding agent of `acp_delegate` acts. |
| [Protected paths](#protected-paths) | Keeps the agent away from its own config, secrets, and database. |
| [Secrets](#secrets) | Keeps API keys out of child processes, logs, and the model input. |
| [Provider keys](#provider-keys) | Sends each API key only to its own host, and keeps it out of errors, exports, and backups. |
| [Toolsets](Tools#turn-toolsets-on-or-off) | Hides tools from a channel. |
| [Telegram access](#telegram-access) | Lets only approved users talk to the bot, and only owners answer approvals. |
| [The skill guard](#the-skill-guard) | Scans skills for dangerous content. |

## The approval gate

The approval gate checks each `terminal` call. It also asks before each `execute_code` script, see
[Scripts of execute_code](#scripts-of-execute_code). The gate is a port of the Hermes Agent
approval flow.

### The order of the checks

1. **Normalize.** The gate applies Unicode NFKC, removes ANSI escape sequences and zero-width
   characters, and changes the command to lower case. Hidden characters then cannot get past the
   rules.
2. **Read the command.** The gate reads the command as bash does. It finds each simple command,
   also the commands inside wrappers such as `sudo` and `bash -c`.
3. **Hardline rules.** These commands are always blocked. No mode and no answer permits them:
   - a recursive delete of `/`, of a system folder, or of the home folder
   - `mkfs`
   - `dd` or a redirection to a raw block device
   - a fork bomb
   - `kill -1`
   - shutdown and reboot commands
   - `tether skills install`, `tether skills update`, and `tether skills uninstall`
4. **Sudo guard.** The gate blocks `sudo -S` when `SUDO_PASSWORD` is not set in `.env` or in
   the process environment. Without a password, the model guesses it.
5. **Gateway self-stop.** In the gateway only, the gate blocks a command that stops or restarts
   the gateway itself. Such a command can never finish, because the stop waits for the running
   turn. The agent tells you to send `/restart` instead. A run that cron started does not get
   this block.
6. **Human-only rules.** A command that matches a human-only rule needs you to tap ✅ Once each
   time. See [Human-only actions](#human-only-actions).
7. **Dangerous rules.** A command that matches one of the 56 dangerous rules needs an approval.
   Examples are `rm -r`, `chmod 777`, `curl … | sh`, `git push --force`, `git reset --hard`, and
   SQL `DROP`. A shell write to a protected path is also dangerous.
   A command that names `state.db` is dangerous, also a read. The `state.db` rules come first,
   then the other protected path rules, then the other rules. The gate looks at all the rules
   that match. The command runs with no question only when each matched protected rule has a
   grant and the first general rule has a grant. Else the gate asks with the first rule that has
   no grant. The hardline and protected path rules read the raw text and each simple command.
   The other rules read the command view: the command with the comments and the plain text of
   `echo`, `printf`, `cat`, and `tee` removed.
8. **No match.** The command runs.
9. **Could not read.** A command that the gate cannot read asks each time, with no grant and no
   judge. See [Commands that the gate cannot read](#commands-that-the-gate-cannot-read).
10. **Live mode.** The mode of the topic (`/mode`) applies. The topic mode `full` counts as
    `approvals.mode: off`.
11. **Grants.** An earlier answer can cover the command.
12. **Cron, smart judge, and the question.** Then come the cron setting, the smart judge, and the
    question to you.
13. **Check again.** After your tap, the gate reads the topic mode again. If the mode is now
    `read-only`, the command does not run. The model gets "The mode of this topic changed to
    read-only while the approval waited."

### Human-only actions

A human-only action needs you to tap ✅ Once each time. There is no ✅ Session, no ✅ Always, no
grant, and no smart judge. `approvals.mode: off`, `/mode full`, and `approvals.cron_mode: approve`
do not lower this level. The gate blocks the action in a cron run and in a member turn. `/approve
all` leaves the prompt open.

The built-in shell rules are a password change, an account creation, a new credential access
(for example `gh auth login` or a write to `authorized_keys`), and a permanent deletion (for
example `shred`). An MCP tool is human-only when its name has a payment, account, credential,
password, purge, or bulk delete word. `approvals.human_only` raises more rules. An entry is the ID
of a dangerous rule, for example `git_force_push`, or `mcp:<server>.<tool>`. See the
[full rule list](https://github.com/dnwandana/tether-agent/blob/main/docs/SECURITY.md#human-only-actions).

The rule `human_only_stage_credential` covers the staging of a credential file, for example
`git add .env` or `git add -A` when the work tree has a credential file. For a broad form, the
gate lists the files with `git rev-parse` and `git ls-files`. It runs the system git with safe
`-c` options and a limit of 5 s. It compares the stat data of each tracked credential file with
the index. The gate never runs `git add`, so git starts no filter driver.

The check is "unchecked" when it cannot tell the files, for example:

- The command sets a `GIT_*` variable, `HOME`, or `XDG_CONFIG_HOME`.
- The folder is unknown, for example after `cd "$DIR"`.
- A broad add comes after a part that can write a file.
- git fails with an error other than "not a git repository", or the limit of 5 s ends.

An unchecked command is human-only, and the prompt shows
`Tether Agent could not check which files this command stages.` `git commit -a` stages only
tracked files, so a credential file that git already tracks is out of scope. See the
[details](https://github.com/dnwandana/tether-agent/blob/main/docs/SECURITY.md#human-only-actions).

### Commands that the gate cannot read

The gate cannot read a command that is longer than 32 KB, has a syntax error, nests too deep,
starts more than 4 new parses, has more than 16 wrappers, or builds a program name at run time.
It also cannot read a command with a glob in a program name, a wrapper option that it does not
know, or more than 256 words from brace expansion.
Such a command gets the rule `could_not_read`. It asks each time, with no grant and no judge. A
cron run blocks it, also with `approvals.cron_mode: approve`. `approvals.mode: off` and `/mode
full` run it. The text for the model adds "Use write_file for large content." See the
[details](https://github.com/dnwandana/tether-agent/blob/main/docs/SECURITY.md#commands-that-the-gate-cannot-read).

### Wrappers

A wrapper is a program that runs another command, for example `sudo`, `env`, `bash -c`, `ssh`, or
`docker exec`. The gate reads through the wrapper and checks the inner command. A privilege or host
wrapper (`sudo`, `ssh <host>`, `docker exec`) has its own grant key, so an answer for `rm -r x`
does not cover `sudo rm -r x`. The question shows the wrapper, for example
`recursive delete (inside sudo)`. See the [wrapper table](https://github.com/dnwandana/tether-agent/blob/main/docs/SECURITY.md#wrappers).

### Approval modes

`approvals.mode` sets the result for a dangerous command:

| Mode | Result |
|---|---|
| `manual` (default) | The gate asks you. |
| `smart` | The default fallback chain, which starts at the primary provider, judges the command first. It has 30 seconds (`approvals.smart_timeout_s`). On `APPROVE`, this one command runs. The next command of the same rule goes to the judge again. On `ESCALATE` or on an error, the gate asks you. A command that names `state.db` never goes to the judge. |
| `off` | The command runs with no question. The hardline rules, the sudo guard, and the human-only rules still apply. In the gateway, the block on a stop or a restart of the gateway also still applies. |

`approvals.mode` is the default for the whole process. The `/mode` of a chat or a topic wins over
it in that topic.

### Modes for a chat or a topic

The owner sends `/mode` to set the approval mode of the chat or the topic where the owner writes.

| Mode | Result |
|---|---|
| `read-only` | The agent reads, searches, and answers. It cannot write files, run commands, or change memory, skills, or cron jobs. |
| `guarded` (default) | The gate follows `approvals.mode`. |
| `full` | The gate asks no question for a dangerous command. The hardline rules, the sudo guard, and the human-only rules still apply. |

`/mode full` asks for a confirm. The mode survives `/new` and a restart. All users of a topic
share one mode. A cron run has no mode. A child agent copies the mode of its parent. A read-only
topic starts no background review. See the [full rules](https://github.com/dnwandana/tether-agent/blob/main/docs/SECURITY.md#modes-for-a-chat-or-a-topic).

### Cron runs

No user is present in a cron run. `approvals.cron_mode` sets the result for a dangerous command in
a cron run:

- `deny` (default): the gate refuses the command.
- `approve`: the command runs.

Two cases come first. When `approvals.mode` is `off`, the command runs. When each matched rule
has a grant, the command runs. A child agent of a cron job follows the same rules.
A cron run always blocks a human-only command and a command that the gate cannot read, also with
`approve`. It also blocks an MCP `write` or `send` call under `deny`.

A cron run also blocks these sends, also with `approve`:

- An MCP `send` call with a scan finding. See [Outbound scan](#outbound-scan).
- A send that `model_guard` holds. See [model_guard and draft-only jobs](#model_guard-and-draft-only-jobs).
- Each MCP write and send of a job with `draft_only`, except a draft tool.

### Scripts of execute_code

Each `execute_code` script needs an approval for the rule `execute_code`, with the description
`run a Python script on the host`.

- In manual mode, each new script asks. A `session` answer covers only the same script bytes.
  Use smart mode to let the judge review a new script first.
- A script with a literal dangerous command or a protected path asks each time. The question
  names the rule, for example `run a Python script on the host (recursive delete)`.
- A script has no `always` answer.
- In smart mode, the judge gets at most the first 8000 characters (`approvals.smart_script_max_chars`). A longer script goes to you.
- In a cron run, each script follows `approvals.cron_mode`, with or without a literal match. With
  `deny`, the gate refuses the script. The refusal says to move the code to a cron script, or to
  set `approvals.cron_mode: approve`. With `approve`, the script runs.
- A script with a literal match of a protected path never goes to the judge. The gate asks you.

### Protected paths and the judge

A protected-path rule never goes to the smart judge. The gate always asks you. A protected-path
rule has no `always` answer.

### Member turns

An approval request from a member turn reads no stored `always` or `session` answer. It saves
none. Only an owner can answer it. Refer to [Owners and members](#owners-and-members). A
human-only request of a member turn is blocked.

### The /approvals command

`/approvals` is an owner-only command. It lists the `always` answers that the gate stores. Each row
shows the rule, the date, and a short code. A row of a command also shows a short preview of the
command and its workdir. A group chat shows no preview and no workdir. The list shows the newest 20
rows. On Telegram, each of these rows has a **Remove N** button. After a remove, the message shows
the rows that are left. With 2 or more rows, a **🗑 Remove all** button removes all answers in
the list. The bot asks first. In the CLI, send `/approvals remove <number> <code>` or
`/approvals remove all <list code>`. The gate asks again for each rule after you remove its
answer. A remove in the CLI does not clear a `session` answer in the gateway. That answer ends
with its session. If the list changed, the command removes nothing and shows the current list. It
tells you when the answer is already removed, for example after a double tap.

### Approval log

The gate writes a row to the `approval_log` table for each decision on a request that matched a
rule, and for each MCP call that is not a read. A row shows the time, the decision, the source, the
answer, the rule, and a redacted preview of at most 300 characters. A command with no match writes
no row. A failed write never changes a decision.

- `/approvals log` shows the last 15 decisions of this chat or topic.
- `/approvals log all` shows the last 15 decisions of all chats. It works only in the CLI and in a
  private chat.
- `approvals.log_retention_days` (default `180`) sets the age at which the gateway deletes a row.
  `0` keeps all rows.

See the [full rules](https://github.com/dnwandana/tether-agent/blob/main/docs/SECURITY.md#approval-log).

### Your answer

The gate waits `approvals.timeout_s` seconds for your answer. The default is 600 seconds. No answer
is a denial.

| Answer | Effect |
|---|---|
| `once` | Permits this command only. |
| `session` | Permits the same rule for the rest of the session. |
| `always` | Permits the same rule for the same command from now on. The row binds the command, the workdir, and the programs. The program saves it in `state.db`. |
| `deny` | Refuses the command. The model gets `BLOCKED by user: <pattern name>` and an instruction to stop and wait for you. |

Some commands do not have all answers:

- A protected path rule and an `execute_code` script have no `always` answer. A protected path
  rule reads only the `session` answers. An `Always` row that an older version saved does not
  permit it.
- A command has no `always` answer when the gate could not read it, when a program did not
  resolve, when the command has run-time text such as `$VAR`, or when the command changes `PATH`.
  A command with a credential in it has only `once` and `deny`.
- A human-only request has only `once` and `deny`.
- A grant covers one rule. A grant for a protected path does not cover another dangerous rule
  in the same command.
- A command that names `state.db` has only `once` and `deny`. It asks each time.
- Only an owner can answer. See [Owners and members](#owners-and-members).

### What you see in the CLI

```
⚠ Approval needed
  <summary>
  Flagged: <pattern name>
  <the command>
[o]nce / [s]ession / [a]lways / [d]eny:
```

The summary is the `description` that the agent gives with the command. With no `description`,
the CLI shows the pattern name and one line of the command. A human-only request has the first
line `🔒 Human-only action` and the choices `[o]nce / [d]eny`. An MCP `send` call has the first
line `📤 Send on your behalf?`.

When a child agent asks, the question also shows `(from subagent N)`. Ctrl-D is a denial.

A permission request of a coding agent has no `always` answer. The prompt is
`[o]nce / [s]ession / [d]eny:`. A request with no session answer shows `[o]nce / [d]eny:`.
See [Coding agents](#coding-agents).

The command shows each control or format character as a mark, for example `<U+202E>`. A line
break and a tab stay. Such a character can hide a part of the command or change its order on the
screen.

### What you see in Telegram

The bot sends a message with the title **Approval needed**, a short summary when the agent gives
one, the reason for the flag, and the command. The message has four buttons:

- First row: ❌ Deny
- Second row: ✅ Once, ✅ Session, ✅ Always

❌ Deny is alone on the first row. The allow buttons share the second row.

The first line of the message shows the kind of request:

- **🛡 Approval needed** for a dangerous command, a script, or an MCP `write` call.
- **🔒 Human-only action** for a human-only request. The flagged line adds "No mode, grant, or
  judge can approve this. Tap each time." The keyboard has two buttons: ❌ Deny and ✅ Once.
- **📤 Send on your behalf?** for an MCP `send` call. The keyboard has ❌ Deny and ✅ Once.

When the match is inside a wrapper, the flagged line names it, for example
`recursive delete (inside sudo)`. A prompt for an MCP call shows one `key: value` line for each
argument.

A permission request of a coding agent has three buttons: ❌ Deny, ✅ Once, and ✅ Session. A
request with no session answer has two buttons: ❌ Deny and ✅ Once. A command with no `always`
answer has no ✅ Always button.

After your answer or after the timeout, the bot edits the message. It shows the result, for
example "✅ **Approval allowed** · this session", the summary, and the command in a closed quote.
Tap the quote to see the full command. Like the CLI, the message shows each control or format
character of the command as a mark. A link in the message gets no preview.

You can also answer with a typed command:

- `/approve` permits the oldest pending command once.
- `/approve session` or `/approve always` gives a wider scope.
- `/deny` refuses the oldest pending command.
- Add `all` to answer all pending commands, for example `/approve all` or `/deny all`.
- `/approve` and `/deny` answer only the approvals of the same topic.

These commands also work while a turn waits for the answer.

On a coding agent request, `/approve always` gives `session`. When the request has no session
answer, it gives `once`.

### MCP tool classes

Each MCP tool has a class. The gate reads the class from the tool name and the hints of the
server.

| Class | Check |
|---|---|
| `read` | The call runs. The gate writes no log row. |
| `write` | The gate asks. ✅ Once, ✅ Session, and ✅ Always are possible. |
| `send` | The gate asks each time, with ❌ Deny and ✅ Once. The header is "📤 Send on your behalf?". |
| `human-only` | You tap ✅ Once each time. See [Human-only actions](#human-only-actions). |

- `mcp.servers.<name>.tools.include` and `tools.exclude` hide tools from the model. An exclude
  wins over an include.
- `tools.class` sets the class of one tool to `read`, `write`, or `send`. It cannot change a
  human-only tool.
- `approvals.human_only` can raise a tool with the entry `mcp:<server>.<tool>`.
- The gate has no smart judge for MCP calls. A call that is not `read` fails when no gate is
  present.
- A `write`, `send`, or `human-only` call is never sent again after a broken connection. The model
  gets "The mcp_<server>_<tool> call may have run. Do not send it again."

See the [classifier rules and the grant key](https://github.com/dnwandana/tether-agent/blob/main/docs/SECURITY.md#mcp-tool-classes).

## Coding agents

The `acp_delegate` tool gives a coding task to an external coding agent, for example Claude
Code or Codex. The coding agent runs as an adapter process on the host, as the same user as
Tether Agent. Only a chat session can call the tool. A cron job and a child agent cannot call
it. Refer to [Tools](Tools#coding-agents).

### Approvals of a coding agent

Each permission request of the coding agent comes to you as an approval message.
`approvals.mode`, smart mode, and `approvals.cron_mode` do not apply. Also with
`approvals.mode: off`, Tether Agent asks you. Two rules of the approval gate apply to a
request of any kind that has a command:

- A command of the hardline list is refused with no question. The progress line shows
  `hardline: <description>`. An earlier `session` answer cannot permit it.
- A command that matches a dangerous rule asks each time and has no session answer. The
  message names the rule,
  for example `claude wants to run a command (dangerous: recursive delete)`.

| Answer | Effect |
|---|---|
| `once` | Permits this request only. |
| `session` | Permits the same kind of action in the same ACP session. For a command, the rule also holds the program name. |
| `deny` | Refuses the action. |

There is no `always` answer. Tether Agent never selects an "always" option of the coding agent,
so the agent does not write a durable rule into its own settings.

These rules apply to `session`:

- For an edit, a read, a fetch, or another kind that is not a command, `session` permits all
  requests of that kind in the same ACP session.
- For a command, `session` on `go test ./...` also permits `go build`, but not `rm`.
- `session` on `rm notes.txt` also permits `rm old.txt`, but `rm -r build` asks.
- `/new`, `/reset`, and `/delete` remove the `session` answers.

These requests have no session answer. They ask each time:

- a compound command: a command with `&`, `|`, `;`, `$(`, a backtick, `<`, `>`, or a new line
- a wrapper program, for example `sudo`, `env`, `bash`, `xargs`, `nohup`, or `timeout`
- a program name with shell syntax, for example a quoted name or `$SHELL`
- a program word with a `/`, for example `./ls` or `/usr/bin/go`
- a command that starts with a `NAME=value` word, for example `LD_PRELOAD=...` or `PATH=...`
- a command request with no command text
- a request to leave plan mode
- a command that matches a dangerous rule, for example `rm -r build`

No answer in `approvals.timeout_s` refuses the action. `/stop` cancels the open requests. The
[security model](https://github.com/dnwandana/tether-agent/blob/main/docs/SECURITY.md#coding-agents-acp)
gives the full list of wrapper programs.

### The limits of a coding agent

- **The preset sets what the agent asks.** `acp.agents.<name>.permission_preset` sets the mode
  of the coding agent. From strict to loose, the presets are `all`, `risky`, `auto`, and
  `folder`. With `folder`, the agent asks for nothing. The model can choose a stricter preset,
  but not a looser one. When the agent reports another mode after the change, and that mode is
  not stricter, the task does not start.
- **The work folder is not a sandbox.** The task must use a folder inside
  `acp.workdir_allowlist`. An empty allowlist refuses each call. The coding agent itself can
  still write outside the folder.
- **The adapter gets no secrets.** The adapter process gets the child environment, so the
  coding agent uses the login of the host user. Tether Agent never reads, stores, or sends a
  login token of the coding agent.
- **A process can stay alive.** A stop ends the process group of the adapter. A program that
  the coding agent starts with `setsid` leaves the group. Under `tether chat` with no systemd,
  such a program can stay alive after Tether Agent stops.

## Protected paths

The file tools (`write_file`, `patch`, and `skill_manage`) refuse to write to these paths. The
approval gate treats a shell write to them as dangerous.

- `~/.ssh/`
- `~/.tether/.env`
- `~/.tether/config.yaml`
- `~/.tether/state.db`, with its `-wal` and `-shm` files, and `state.db.lock`
- `~/.tether/memory/memory.md` and `~/.tether/memory/user_profile.md` (only the `memory` tool
  writes them)
- `~/.tether/skills/` (only `skill_manage` can write there)
- `~/.tether/scripts/` (only you put cron scripts there)
- `~/.tether/backups/`
- the shell start files, the tool credential files, and the credential folders in the home
  folder, for example `~/.bashrc`, `~/.bash_aliases`, `~/.gitconfig`, `~/.aws/`, and `~/.gnupg/`

The [security model](https://github.com/dnwandana/tether-agent/blob/main/docs/SECURITY.md#protected-paths)
gives the full list.

The file tools resolve `..` and symbolic links before the check. A link that points to a protected
path is refused. The check ignores case. For example, `~/.SSH/authorized_keys` is refused.

The shell check reads the text of the command only. It does not resolve a relative path or a
symbolic link. A shell write is a redirection or a write command, for example `cp`, `rm`,
`sed -i`, `perl -i`, `sponge`, or `sqlite3`.

Each command that names `state.db` is dangerous, also a read. Its description is
`use of the Tether Agent database`. Such a command asks each time.

`view_image` and `transcribe_audio` send a file to a provider, so they also refuse to read a
protected path. The error is
`<path> is a protected path. The tool does not send it to the provider.`

Tether Agent scans each entry of `memory.md` and `user_profile.md` when it builds the prompt.
A blocked entry becomes a `[BLOCKED m-<id>: …]` line. Run `tether doctor` to see the blocked
entries.

## Secrets

### The .env file

- Keep your secrets in `~/.tether/.env`. The config refers to them with `${NAME}`.
- Set the file mode to 0600. The program does not start when the group or other users have any
  permission on the file.
- A variable in the process environment overrides the same name in `.env`.
- A bound secret takes its value from `.env` only. `.env.lock` in the data folder is a protected
  path.

### Child processes get no secrets

These child processes get an environment with no secrets:

- `terminal`
- `execute_code`
- cron scripts
- MCP stdio servers
- media commands
- goal gates
- ACP adapter processes (`acp_delegate`)

A name is a secret when one of these is true:

- `.env` has the name.
- The name ends in `_KEY`, `_TOKEN`, or `_SECRET`.
- The name contains `PASSWORD`.

These names pass through to the child:

- the names in `terminal.env_passthrough`
- the `env` block of an MCP server, for that server only
- the `env` block of an ACP agent entry (`acp.agents.<name>.env`), for that adapter only. A
  secret name in this block is a config error.

A skill cannot pass a name. The `required_environment_variables` of a skill only give a
status: `passed`, `not passed: add it to terminal.env_passthrough in config.yaml`, or
`not set`. To give a secret to a skill, bind it in `exec_secrets`.

### Bound secrets

A bound secret goes only to the commands that you name:

```yaml
exec_secrets:
  PYPI_TOKEN:
    commands: ["twine upload", "uv publish"]
```

A name in both `exec_secrets` and `terminal.env_passthrough` is a config error. Use
`terminal.env_passthrough` only for a name that each command needs.

A bound secret goes only to a command text that is one simple command. The secret goes to the
whole shell, so a second part of the text could read it. Only fixed wrappers can come before the
bound command: `timeout`, `nice`, `nohup`, `stdbuf`, `setsid`, `ionice`, `taskset`, `chrt`,
`flock` with a program, `time`, and `env` with a program and no option. The command words can
name the variable, for example `twine upload -p "$PYPI_TOKEN" dist/*`.

These commands get no secret:

- two or more commands, for example `cd dist && twine upload *` or
  `twine upload dist/* | tail -n 20`
- a subshell, a group, a loop, a function, or a command substitution
- an assignment before the command
- another wrapper, for example `bash -c`, `sh`, `eval`, or `xargs`
- a redirect to `/dev/tcp/` or `/dev/udp/`, because that output goes to the network
- a redirect target that the shell builds at run time, for example `> "$LOG"`
- a here-document or a here-string with an expansion

A command that does not match still runs, but it gets no secret. The result ends with a line
such as `[PYPI_TOKEN was not given: "curl" is not a bound command]`. To run more than one
command with a secret, put the commands in a script. Bind the absolute path of the script, and
run the script by that path. The script needs a `#!` line and the exec bit (`chmod +x`).
`bash <script>` gets no secret. See the
[reasons](https://github.com/dnwandana/tether-agent/blob/main/docs/SECURITY.md#the-match-rule).

The first use of a secret with a program asks `🔑 Give PYPI_TOKEN to this program?` The prompt
shows the real path and the hash of the program. Tap ✅ Allow this program to save the pin. A new
path or a new hash asks again. A cron run with no pin gets no secret. In a group, the prompt goes
to your private chat.

A credential that a command prints becomes a session handle, for example `$TMP_SECRET_1`. A later
command can use the handle. A handle lives in memory only, and it ends with the session or a
restart. After outside content in the turn, a command with a handle asks first.

When a bound secret has no value, the agent calls `request_secret`. You get the card
`🔑 Secret needed: PYPI_TOKEN` in a private chat. Reply with the value. The bot deletes your reply
and saves the value to `.env`. The model never sees the value.

- While a card waits, a value that starts with `/` goes to the card, unless it is a known command.
- The bot deletes an edit of your reply. The edit never reaches the agent.
- The CLI reads the value with no echo. When the read stops, it prints
  `The secret read stopped. Press Enter to continue.`

These commands work in a private chat:

- `/secrets` lists each bound name, its state, and its pins.
- `/secrets set NAME` saves a new value.
- `/secrets unpin NAME` deletes the pins of a name.

After a failed command that got a secret, a clear auth failure (a 401, or a 403 with an auth
word) marks the secret. The mark shows in `/secrets` and in `tether doctor`.

### Redaction

The redactor masks API keys, tokens, and credentials in the logs, in tool output, in error text,
and in the text that goes to the model.

- A token of fewer than 18 characters becomes `***`.
- A longer token keeps its first 6 and its last 4 characters.
- The redactor masks the password in an `http` or `https` URL, the value of `Authorization: Basic`,
  and the values of `--password` and `--token`. These values become `***`.
- The redactor also masks the exact values from `.env` and the values of the secret names in the
  process environment. It ignores values of fewer than 8 characters. This rule also covers each
  provider key.
- An exact value becomes its label, for example `«PYPI_TOKEN»`. A read of `.env` shows
  `PYPI_TOKEN=«PYPI_TOKEN»`.

### Backups

`tether backup` writes one archive with the mode 0600. These rules keep the secrets out of it:

- The archive never contains `.env` or `.history`.
- A literal key in `providers.<name>.api_key`, `telegram.bot_token`, an MCP `env` or
  `headers` block, or an `acp.agents.<name>.env` block becomes a `${NAME}` reference.
- The backup masks each known secret value in the messages, the stored reasoning,
  the memory files and the memory notes, the skills, and the scripts.
- `tether restore` lists the variables that `.env` must contain. The list has each `${NAME}` of
  `config.yaml`, and the key env of each preset entry with no `api_key`.

Refer to [Running as a Service](Running-as-a-Service#back-up).

## Outbound scan

The scan masks secrets, card numbers, IBANs, and phone numbers in a send to a target that is not
private. A private target is the CLI, `local`, the private chat of an owner, and each chat id in
`outbound.private_chats`. The scan covers chat answers, the files of an answer, cron deliveries,
and MCP `send` calls.

The detectors give these labels:

- a secret: `[secret]`, or `«NAME»`
- a card number: `[card ••••1111]`
- an IBAN: `[IBAN GB82••••5432]`
- a phone number: `[phone +94•••••4567]`

On a match, the send is masked. You get a note in your private chat, for example
`🛡️ I masked 2 items in my answer in "Home ops":`. The button `✅ Post the full answer there`
posts the original. An MCP send with a finding asks with the button `✅ Send redacted`. In a cron
run, the gate blocks the send. `outbound.scan: false` turns the scan off.

## model_guard and draft-only jobs

`model_guard` checks a cron send after the run read outside content. The send is a delivery to a
target that is not private, or an MCP send that `approvals.cron_mode: approve` allows.
`outbound.guard: false` turns it off. `outbound.guard_provider` names the provider entry. The
guard gets only facts: the job name and purpose, the sources, the tool counts, and the kinds of
findings. It never gets the answer text. An answer that is not `allow`, an error, or a timeout is
a hold.

A held delivery waits in `held_sends` for 24 h. You get the card `⏸️ Held: job "<name>"` with the
buttons ❌ Drop and ✅ Send.

A job with `draft_only` can read and write drafts, but it cannot send. The run has no `terminal`,
`execute_code`, browser action tools, `delegate_task`, or `acp_delegate`. MCP read tools and
draft tools run. A draft tool has a draft verb and the noun `draft`. Each other MCP write and send
call is blocked. The job delivers only to a private chat or `local`.

## Untrusted content

The results of `web_fetch`, `web_search`, the browser tools, and the MCP tools go into an
`untrusted_tool_result` block. The block tells the model to treat the content as data, not as
instructions. A result under 32 characters is not wrapped. A forged tag in the content is
changed. An `execute_code` script gets the raw data. The stdout of a cron data script uses the
source `cron data script`. A `read_file` of its spill file `cache/spill/cron_data_script-<id>.txt`
is wrapped too.

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

### Owners and members

An owner is a user that pairing, `telegram.allowed_users`, `telegram.group_allowed_users`, or
`telegram.allow_all_users` admits. A user that only `telegram.group_allowed_chats` admits is a
member. A member can talk to the bot and run `/help`, `/new`, `/reset`, `/stop`, `/steer`,
`/now`, `/todo`, `/sessions`, and `/usage`. Only an owner can answer an approval. A member
gets `⛔ Only the owner can answer this prompt.` Refer to
[Telegram](Telegram#owners-and-members) for the full rules.

Add each trusted group member to `telegram.group_allowed_users`.

A member turn has less power than an owner turn:

- A member turn can use only the tools of `toolsets.member`, in a shared session and in a member
  session. The default is `[web, ask, todo]`. A member never gets `cronjob` or `delegate_task`. A call of another tool
  gets the refusal `This tool is for the owner only. A member can use these tools: <list>.`
- The system prompt of a member session has no owner memory.
- A member turn starts no background review, so it cannot write the owner memory or the skills.
- In a shared session, a member text does not steer an owner turn. It waits in the queue as its
  own turn. The texts of one member merge into one waiting turn.
- `/approvals` is an owner-only command.

When other people can talk to the bot, set `gateway.media_delivery_strict: true` in `config.yaml`.
A `TETHER_MEDIA_DELIVERY_STRICT` line in `.env` wins over the key, so remove an old line. In the
default mode, the bot can send any local file that is not protected and not in a denied folder.
A prompt injection can make the model name any file.
A protected path is never sent, also from a folder of `gateway.media_allow_dirs`. This rule
covers the credential folders, for example `~/.aws/` and `~/.gnupg/`.

In the gateway, Tether Agent deletes the files in `~/.tether/cache/media/` and
`~/.tether/cache/screenshots/` after `media.cache_retention_days`. The default is 7 days.

## The skill guard

The skill guard scans a skill before the agent uses it. It has 121 threat rules in 12 groups. A
`tether skills install` always scans the skill in strict mode. The agent can never install a skill
itself. Refer to [Skills](Skills#the-skill-guard).

## Known limits

- **No sandbox.** Commands run on the host, as the Tether Agent user.
- **The gate cannot see what an MCP server does.** It cannot see the effect of an MCP call. It
  uses the class of the tool name: `read`, `write`, `send`, or `human-only`. A wrong name gives a
  wrong class. Enable only the servers that you trust, and hide tools with `tools.exclude`. See
  [MCP tool classes](#mcp-tool-classes).
- **The browser has no check for private addresses.** `web.block_private_networks` applies to the
  web tools only. A browser page can open a local address, for example the page of a router.
- **One browser profile.** All sessions share the cookies and the logins. `browser_cookies` does
  not return the cookie values.
- **Memory and skills are shared.** All users of the bot use one memory and one skill set. A
  Telegram chat sees only its own sessions and cron jobs in `session_search` and `cronjob`.
- **Scripts can build commands.** Each new `execute_code` script asks in manual mode, but the
  gate cannot see a command that the script builds while it runs. A Python file that `terminal`
  runs gets only the shell rules. A command that one step writes and a later step runs, for
  example `echo 'rm -rf x' > s.sh && sh s.sh`, does not ask at `echo`.
- **The fallback chain sends the conversation to other providers.** When an entry fails before
  the first byte, the next entry gets the full request. Put only the providers that you trust in
  `agent.fallback`.
- **Cron scripts run with no approval.** Only files in `~/.tether/scripts/` can run as cron
  scripts, and the agent cannot write there without an approval.
- **Goal gates run with no approval prompt.** You write each gate with `/goal gate add`.
  Tether Agent checks the gate again before each run. A gate that matches a dangerous rule still
  runs.
- **A pin covers only the entry file and its interpreter.** A process can change the file between
  the hash and the run.
- **The raw background log holds a printed value.** The file `cache/spill/bg-<pid>.log` has the
  mode 0600.
- **A bound secret goes only to a command that runs alone.** A pipe to `tail` or a `cd` before
  the command gets no secret. Put the commands in a script, and bind the path of the script.
- **A bound script can pass the value on.** Each command that the script runs gets the value.
- **`git commit -a` can stage a tracked credential file.** The rule `human_only_stage_credential`
  does not cover it.
- **The scan does not cover each send.** It does not cover `curl -d` and other network commands,
  `execute_code` network calls, browser form posts, `web_fetch` URLs, `web_search` queries, an MCP
  `write` call, and a binary file or a file over 1 MB.
- **A network program that is not on the list does not mark a run.** For example, `python
  fetch.py` does not mark the run as a run with outside content, so `model_guard` does not
  check the later send.
- **A coding agent can write outside its work folder.** Its permission preset sets which actions
  it asks for. See [Coding agents](#coding-agents).

## Recommended settings for a VPS

1. Create a separate user for Tether Agent. Do not use root.
2. Set the mode of `~/.tether/.env` to 0600.
3. Keep `approvals.mode` at `manual` or `smart`.
4. Keep `approvals.cron_mode` at `deny`.
5. Keep `web.block_private_networks` at `true`.
6. Hide the `browser` and `code_execution` toolsets from a channel that you do not fully trust.
7. Set `telegram.allowed_users` to your own user id only.
8. Add each trusted group member to `telegram.group_allowed_users`, not only to
   `telegram.group_allowed_chats`.
9. Set `gateway.media_delivery_strict: true` in `config.yaml` when other users can talk to the bot.
   Make sure that `.env` has no `TETHER_MEDIA_DELIVERY_STRICT=0` line.
10. Enable only the MCP servers that you trust. Hide the tools that the agent does not need with
    `tools.exclude`.
11. Send `/mode read-only` in a group topic that members use. The agent then only reads and
    answers there.

```yaml
approvals:
  mode: smart
  cron_mode: deny
  timeout_s: 600
  human_only: [git_force_push]
web:
  block_private_networks: true
toolsets:
  telegram: [terminal, file, web, memory, skills, ask, cron]
```
