# Security model

This document tells how Tether Agent controls what the agent can do. It also gives the known
limits. Read the limits before you give the agent access to a machine that holds important data.

[CONFIG.md](CONFIG.md) gives the default of each key that this document names.

## Summary of the limits

- Commands run on the host, as the user that runs Tether Agent. There is no container and no
  sandbox.
- The approval gate checks shell commands and the permission requests of a coding agent
  (`acp_delegate`). It does not check the tools of MCP servers.
- In manual mode, each `execute_code` script needs one approval for each session. A script with
  a literal dangerous command or a protected path asks each time. The gate cannot find a
  command that the script builds while it runs.
- A cron script job runs with no approval. Only files in `~/.tether/scripts/` can run as
  cron scripts. The file tools cannot write in that folder, and a shell write to it needs an
  approval.
- A goal gate runs before each judge call with no approval prompt. Tether Agent checks the
  gate against the hardline list, the gateway self-stop check, and the sudo guard before
  each run.
- In the default mode, the bot can send any local file that is not protected and not in a denied
  folder (see "Files that the bot sends"). Use strict mode for a bot that other people use.
- The browser tools have no check for private addresses. All sessions share one Chrome profile.
  `browser_cookies` does not return the cookie values.
- In a chat of `telegram.group_allowed_chats`, each member can talk to the bot. Only an owner
  can answer an approval and run the owner commands. See
  [Owners and members](#owners-and-members).
- A coding agent of `acp_delegate` runs on the host as the same user. Its permission
  preset sets which actions it asks for. Tether Agent does not stop a write outside the
  work folder. A hardline command of the coding agent is refused with no prompt.

## Approval gate for commands

The approval gate checks every `terminal` call. It also checks each `execute_code` script (see
[execute_code](#execute_code)), and each tool call of the script goes through the same checks as a
model call. The gate is a port of the Hermes approval flow.

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
   - `tether skills install`, `tether skills update`, and `tether skills uninstall`. Only the
     owner runs these commands, in a terminal.
3. **Sudo guard.** The gate blocks `sudo -S` when no sudo password is set, because the model
   then guesses the password. Set `SUDO_PASSWORD` in `.env` if the agent must use sudo.
4. **Gateway self-stop.** In the gateway, the gate blocks a command that stops or restarts the
   gateway process itself. Examples are `systemctl restart tether`, `tether gateway stop`,
   `pkill tether`, and `tether update --restart`. The stop would cut the turn that runs the
   command. A cron run does not get this check.
5. **Dangerous rules.** The `state.db` rules come first. Then come the other protected path
   rules, then the 56 Hermes dangerous rules. A command that matches one of
   them needs an approval. Examples are `rm -r`, `chmod 777`, `curl … | sh`, and
   `git push --force`. A shell write to a protected path and each command that names
   `state.db` are also dangerous. See [Shell commands](#shell-commands). The gate looks at all
   the rules that match. The command runs with no question only when each matched protected rule
   has a grant and the first general rule has a grant. Else the gate asks with the first rule
   that has no grant. A grant for a protected path does not cover another dangerous rule in
   the same command.
6. **No match.** The command runs.

### Approval modes

`approvals.mode` sets the result for a dangerous command:

- `manual` (the default): the gate asks the user.
- `smart`: a second call to the model judges the risk. The call uses the default fallback
  chain: the primary provider, then the entries of `agent.fallback`. When the key
  `agent.fallback` is absent, the next entry is the entry `fallback`. The call moves to the
  next entry under the rules of
  [the fallback chain](https://github.com/dnwandana/tether-agent/wiki/Providers#the-fallback-chain).
  The gate permits a low-risk command with no question. For other commands, the gate asks the
  user. When the judge call fails, the gate also asks the user.
  This approval covers this one command only. The next command of the same rule goes to the
  judge again. A command that names `state.db` never goes to the judge. For an
  `execute_code` script, the judge gets a script prompt and at most the first 8000
  characters (`approvals.smart_script_max_chars`). A longer script goes to the user.
- `off`: the command runs with no question. The hardline list and the sudo guard still apply.

### Cron runs

No user is present in a cron run. `approvals.cron_mode` sets the result for a dangerous command
in a cron run:

- `deny` (the default): the gate refuses the command with no question.
- `approve`: the command runs.

Two cases apply before `approvals.cron_mode`:

- When `approvals.mode` is `off`, the command runs.
- When each matched rule has a grant, the command runs. See
  [The question and the scopes](#the-question-and-the-scopes). A command that names `state.db`
  never has a grant.

In a cron run, an `execute_code` script with no literal dangerous command runs. A script with
a literal match follows `approvals.cron_mode`. Remove `code_execution` from `toolsets.cron`
for full control.

A child run of a cron job (`delegate_task`) also has no user. It follows
`approvals.cron_mode` like the cron run. A cron run and its children cannot start a
background delegation, because the job ends before the children.

### The question and the scopes

The gate waits `approvals.timeout_s` seconds for an answer. The default is 600 seconds. No answer
is a denial.

The approval message shows a one-line summary that the model writes in the `description`
parameter. The model can write a wrong summary, so the message also shows the command. Telegram
shows at most 3800 characters of it. Decide from the command. The gate does not read the summary.
Tether Agent removes control characters and format characters from the summary.

The user answers with one of these scopes:

- `once`: permits this command only.
- `session`: permits the same rule for the rest of the session.
- `always`: permits the same rule from now on. The program saves the rule id in the
  `approvals_always` table of `state.db`. The running agent never writes its own `config.yaml`.
  A protected path rule has no `always` answer.
- `deny`: refuses the command. The model gets the result `BLOCKED by user: <pattern name>`.

These rules limit the scopes:

- A protected path rule has no `always` answer. A typed `/approve always` gives `session`.
  A protected path rule reads only the `session` grants. It never reads `approvals_always`, so
  an `always` row that an older version saved does not permit it.
- A grant covers one rule. When a command matches more than one rule, the gate asks for the
  first rule that has no grant.
- A command that names `state.db` has no `session` and no `always` answer. It asks each time.
  A saved `always` answer does not apply to it.
- An `execute_code` script has no `always` answer.
- Only an owner can answer an approval. See [Owners and members](#owners-and-members).

A permission request of a coding agent has no `always` answer. Some requests also have
no `session` answer. See [Coding agents (ACP)](#coding-agents-acp).

### Goal gates

A goal gate is a shell command that must pass before the goal judge runs. The owner adds a
gate with `/goal gate add <command>`. Tether Agent checks the command when the owner adds it.
The checks are in this order:

1. **Hardline list and gateway self-stop.** A command of the hardline list is refused. In the
   gateway, a command that stops or restarts the gateway itself is also refused. The reply
   gives the description of the pattern and "This command cannot be a gate."
2. **Sudo guard.** When no sudo password is set, a `sudo -S` command is refused with "sudo
   with a password on stdin cannot be a gate." When a sudo password is set, this check does
   not refuse the command, and the next check applies.
3. **Dangerous rules.** A command that matches a dangerous rule needs a confirmation. An
   example is `sudo -S`, which matches the rule "sudo with privilege flag". Telegram shows the
   **Add gate** and **Cancel** buttons. The buttons stop working after `approvals.timeout_s`,
   and then the gate is not added. The CLI asks with `[y/N]`. The add check does not read
   `approvals.mode`.
4. **No match.** The gate is added at once.

After the add check, these rules apply:

- The gate runs before each judge call, with no approval prompt.
- Before each run, Tether Agent checks the gate again with the hardline list, the gateway
  self-stop check, and the sudo guard. A refused gate does not run. The goal pauses at once
  with the reason, with no retries and no continuation turns.
- A gate that matches a dangerous rule still runs. `state.db` keeps the gates, and no shell
  command can change `state.db` with no prompt. See [Shell commands](#shell-commands).
- The gate gets the child environment, with no secrets. See
  [The child environment](#the-child-environment). In the gateway, it also gets
  `TETHER_GATEWAY=1`.
- The gate runs in the folder where the Tether Agent process started, like a `terminal`
  call with no `workdir`. Write `cd /path && <command>` for another folder.
- The timeout is 300 seconds (`goals.gate_timeout_s`). A timeout and a cancel count as exit -1.
- Only an owner can add a gate. See [Owners and members](#owners-and-members).

## Coding agents (ACP)

The `acp_delegate` tool gives a coding task to an external coding agent, for example
Claude Code, over the Agent Client Protocol (ACP). The agent runs as an adapter process
on the host, as the same user as Tether Agent. Only a chat session can call the tool. A
cron job and a `delegate_task` child cannot call it.

### Approvals

Each permission request of the coding agent comes to you as an approval message.
`approvals.mode`, smart mode, and `approvals.cron_mode` do not apply. Also with
`approvals.mode: off`, Tether Agent asks you. Two rules of the approval gate apply to a
request of any kind that has a command, not only to an `execute` request:

- **Hardline floor.** A command of the hardline list is refused with no prompt. Tether Agent
  selects a reject option of the agent. The progress line shows `hardline: <description>`,
  and the log gets a warning. An earlier `session` answer cannot permit the command.
- **Dangerous rules.** A command that matches a dangerous rule asks each time and has no
  `session` answer. The message names the rule, for example
  `claude wants to run a command (dangerous: recursive delete)`.

The command text is the `command` field of the request. Tether Agent joins a list of words
with spaces.

- The answers are `once`, `session`, and `deny`. There is no `always` answer. Tether Agent
  never selects an "always" option of the agent, so the agent does not write a durable
  rule into its own settings.
- `session` permits the same kind of action in the same ACP session of the coding agent.
  For an edit, a read, a fetch, or another kind that is not a command, it permits all
  requests of that kind. For a command, the rule also holds the program name. So `session`
  on `go test ./...` also permits `go build`, but not `rm`. `session` on `rm notes.txt` also
  permits `rm old.txt`. A later `rm -r build` asks, and `rm -rf ~` is refused.
- These requests have no `session` answer. They ask each time.
  - A compound command: a command with `&`, `|`, `;`, `$(`, a backtick, `<`, `>`, or a new
    line. This includes `&&`, `||`, redirects, and process substitutions.
  - A wrapper program: `sudo`, `env`, `sh`, `bash`, `zsh`, `dash`, `ksh`, `fish`, `csh`,
    `tcsh`, `busybox`, `xargs`, `nohup`, `timeout`, `nice`, `exec`, `eval`, `command`,
    `builtin`, `time`, `setsid`, `doas`, `su`, `runuser`, `pkexec`, `stdbuf`, `ionice`,
    `chroot`, `unshare`, `nsenter`, `flock`, `watch`, `script`, `strace`, `taskset`, or
    `chrt`.
  - A program name with shell syntax, for example a quoted name or `$SHELL`.
  - A program word with a `/`, for example `./ls` or `/usr/bin/go`. A path can point to any
    file.
  - A command that starts with a `NAME=value` word, for example `LD_PRELOAD=...` or
    `PATH=...`. Such a word can change what the program runs.
  - A command request with no command text.
  - A request to leave plan mode.
  - A command that matches a dangerous rule, for example `rm -r build`. This holds for a
    request of any kind that has a command.
- No answer in `approvals.timeout_s` refuses the action. `/stop` cancels the open requests.
- `/new`, `/reset`, and `/delete` remove the `session` answers.

### Permission presets

`acp.agents.<name>.permission_preset` sets the mode of the coding agent. The presets from
strict to loose are `all`, `risky`, `auto`, and `folder`. With `folder`, the agent asks for
nothing. The model can choose the config preset or a stricter one, but not a looser one. So
a text in a repository cannot make the model turn off the approvals. Tether Agent sets the
mode again before each task. When the agent reports another mode after the change, and that
mode is not stricter, the task does not start. Tether Agent then does not use that ACP
session again, and the next task starts a new one.

### Work folders

The coding agent works only in a folder inside `acp.workdir_allowlist`. Tether Agent
resolves the symbolic links of the folder and of each allowlist entry before the check. An
empty allowlist refuses each call. The agent itself can still write outside the folder. Its
preset and its own checks apply there.

### The adapter environment

The adapter process gets the child environment, with no secrets. So `ANTHROPIC_API_KEY`
and `CLAUDE_CODE_OAUTH_TOKEN` do not reach it, and the agent uses the login of the host
user. The `env` block of the agent entry cannot hold a secret name. Tether Agent never
reads, stores, or sends a login token of the coding agent.

### Known limits

- A stop sends SIGTERM and then SIGKILL to the process group of the adapter. A program that
  the coding agent starts with `setsid` leaves the group. Under systemd, the stop of the
  service ends it. Under `tether chat` with no systemd, it can stay alive after Tether
  Agent stops.
- A SIGKILL of the `tether` process leaves the adapter alive outside systemd.
- Telegram can show the approval message of an action before the status line of that action.

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

## Memory and background review

`memory.md` and `user_profile.md` go into the system prompt of each later session. A skill
description goes into the skills index of the prompt. So text that the agent saves there stays
active after `/new`, after a restart, and after a model change.

- The background review reads the full conversation, also tool results (for example web pages and
  command output) and group messages that were not sent to the agent. The review prompt tells the
  model that this text is data, and that it must not save an instruction from it.
- The `memory` tool scans the text of each `add` and `replace` with the memory threat table.
  The table has 25 rules of Hermes `tools/threat_patterns.py`, 4 credential rules of the skill
  guard, and a check for invisible characters and Unicode tag characters. Any finding refuses
  the write. A `remove` is never refused. A text over the limit of 4000 characters for one entry
  (`memory.entry_max_chars`) gets the length error, with no scan.
- The notice of a review write to `memory.md` or `user_profile.md` shows the full saved text,
  for example ``💾 Memory updated: `VPS = ovh` ``. A hidden character shows as a mark, for
  example `<U+200B>`. So the user sees each character that goes into the system prompt. The
  notice of a write to a note shows only the topic, for example `💾 Memory note updated: rpc`.
- The memory store reads `memory.md`, `user_profile.md`, and the notes only as regular files. It
  does not follow a symbolic link, and it does not open a FIFO or a device. All reads and writes of
  notes stay in the `notes/` folder that the store examined, also when a link replaces the folder
  after the check.
- `<data>/memory/memory.md` and `<data>/memory/user_profile.md` are protected paths.
  `write_file` and `patch` refuse them, and a shell write to them needs an approval. So the
  `memory` tool, with its scan and its notice, is the only normal writer.
  `<data>/memory/notes/` stays writable.
- When Tether Agent builds the system prompt, it scans each entry of the two files again. An
  entry with a finding, or an entry over 4000 characters, does not go into the prompt. The
  prompt gets this line in its place:
  `[BLOCKED m-<id>: this entry matched the memory threat scan (<ids>). Remove it with the memory tool: remove m-<id>.]`
- `<id>` is the first 8 hex characters of the SHA-256 hash of the entry. A long entry gets the
  finding ID `too_long`. The file keeps the entry. The agent logs a warning for each blocked
  entry.
- The `search` results of the `memory` tool show the same line, not the entry.
  `remove m-<id>` removes the entry. `tether doctor` lists the blocked entries.

## Sessions and cron jobs of other chats

`session_search` and `cronjob` show only the data of the chat of the caller:

- The CLI, and a cron job that was made in the CLI, see all sessions and all jobs.
- A Telegram chat sees only its own sessions, the forks of those sessions, and the runs of its own
  cron jobs. A DM also sees the topics of the same DM. A group with one session for each user
  (`telegram.group_sessions_per_user`) gives each user only their own sessions.
- A Telegram chat can list, change, pause, run, and remove only the cron jobs that it made. A job
  of another chat gives the same error as a job that does not exist.
- From a Telegram chat, `deliver` can be `origin`, `local`, or `telegram:<chat_id>` of the same
  chat. The CLI can deliver to any chat.
- A cron run gets the scope of the chat that made its job.

The memory files and the skills are one set for all users. Do not give the `memory` or `skills`
toolsets to people whom you do not trust.

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
- goal gates
- ACP adapter processes (`acp_delegate`)

A name is a secret when `.env` has it, or when it ends in `_KEY`, `_TOKEN`, or `_SECRET`. A name
that contains `PASSWORD` is also a secret. The check ignores case.

These names pass through to the child:

- the names in `terminal.env_passthrough`
- the `env` block of an MCP server entry, for that server only
- the `env` block of an ACP agent entry, for that adapter only. A secret name in this block is a
  config error.

A skill cannot pass a name. The `required_environment_variables` of a skill give only a
status line in the `skill_view` result and in the text of a skill slash command, for example
`Required environment variables: GITHUB_TOKEN (passed), AWS_SECRET_ACCESS_KEY (not passed: add it to terminal.env_passthrough in config.yaml).`
Each name has one of these statuses:

- `passed`: the name is in `terminal.env_passthrough` and has a value, or the name is not a
  secret and the process has it.
- `not passed: add it to terminal.env_passthrough in config.yaml`: the name is a secret with a
  value, and the list does not have it.
- `not set`: the process and `.env` have no value.

The model can write a skill with `skill_manage`, and the author of a skill is not the owner.
To give a secret to a child, put its name in `terminal.env_passthrough`.

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

### Provider keys

- The wizard writes each provider key to `.env`, not to `config.yaml`.
- An entry with a `preset` reads the key env of the preset only when its `base_url` is the URL
  of the preset. The key of a preset never goes to another host.
- The provider client removes the API key from the error text of a failed call. It keeps at
  most 512 bytes of the error body.
- When the chain moves a call to another entry, that provider gets the full conversation. The
  conversation includes the tool output and the memory in the system prompt.
- The `openrouter` preset on the preset URL also sends the attribution headers `HTTP-Referer`
  and `X-Title`.

These actions send a model list call to an entry:

- the setup wizard
- `/model <entry>`
- the ↻ Refresh button of a model list, or `/model <entry> refresh`

A model list call is a GET with the API key of the entry. These rules apply:

- The URL is `<base_url>/models`. For the `anthropic` format, the URL is
  `<base_url>/v1/models?limit=1000`, with the `/v1` rule of `base_url` in CONFIG.md.
- The call sends the same auth headers as a chat call.
- The key goes only to the base URL of the entry.
- The `openrouter` preset on the preset URL gets two list calls.
- In the wizard, a search text goes to OpenRouter as `/models?q=<text>`.
- Each call stops after 10 seconds (`provider.model_list_timeout_s`).
- A failed call shows only a short cause, for example `HTTP 401` or `no connection`. The reply
  never shows the response body or the URL.

The gateway and the CLI chat keep the model lists in a cache:

- The cache is in memory only. No list goes to disk.
- A list stays in the cache for 1 hour (`provider.model_cache_ttl_s`).
- Only a list from a successful call goes into the cache.
- The cache key holds the first 16 hex characters of the SHA-256 hash of the API key, not the
  key.
- Refresh removes the list of the entry. A restart removes all lists.

### Backups

`tether backup` writes the data to one archive. `tether restore` puts the data from an archive
back in the data folder.

- `tether backup` writes the archive with mode 0600. The archive holds every message.
- The archive never holds `.env` or `.history`.
- The backup replaces a literal key in `providers.<p>.api_key`, `telegram.bot_token`,
  `mcp.servers.<s>.env`, `mcp.servers.<s>.headers`, and `acp.agents.<a>.env` with a
  `${NAME}` reference.
- The backup masks each known secret value in the messages, the reasoning text of the
  messages, the memory files and the memory notes, the skills, and the scripts. The list of
  known values is the same list that the redactor uses.
- The backup replaces each known secret value in the other strings of `config.yaml` with a
  `${NAME}` reference to its variable. The value works again after a restore when `.env` has
  that name.
- The backup reports other values in `config.yaml` that look like a key. It does not change
  these values.
- `tether restore` lists each variable that the restored `config.yaml` needs, and tells you
  which variables are not set. The list includes the key env of each preset entry with no
  `api_key`, when the `base_url` of the entry is empty or the URL of the preset.
- `tether restore` refuses links, absolute paths, `..` paths, and names that are not tether
  data files.
- `tether restore` keeps the replaced data in `<data>/pre-restore-<time>/`. This folder holds
  the data without masks.

## Protected paths

`write_file`, `patch`, and `skill_manage` refuse to write to these paths. The approval gate
also treats a shell write to them as dangerous. `<data>` is the data folder, `~/.tether` by
default.

- `~/.ssh/`
- `<data>/.env`
- `<data>/config.yaml`
- `<data>/state.db`, and also `<data>/state.db-wal` and `<data>/state.db-shm`
- `<data>/memory/memory.md` and `<data>/memory/user_profile.md` (only the `memory` tool writes
  them)
- `<data>/skills/` (only `skill_manage` can write there)
- `<data>/scripts/` (only the user can put cron scripts there)
- `<data>/backups/` (an archive in it can install skills and scripts on restore)
- `<data>/state.db.lock`
- the shell start files in the home folder: `~/.bashrc`, `~/.bash_profile`, `~/.bash_login`,
  `~/.bash_logout`, `~/.bash_aliases`, `~/.profile`, `~/.zshrc`, `~/.zshenv`, `~/.zprofile`,
  `~/.zlogin`, and `~/.zlogout`
- the start files of X and vim in the home folder: `~/.xinitrc`, `~/.xprofile`, and `~/.vimrc`
- the tool credential and configuration files in the home folder: `~/.netrc`, `~/.pgpass`,
  `~/.npmrc`, `~/.pypirc`, and `~/.gitconfig`
- the folders in the home folder that start programs at login or configure a shell or git:
  `~/.config/systemd/`, `~/.config/autostart/`, `~/.config/environment.d/`, `~/.config/fish/`,
  `~/.config/git/`, and `~/Library/LaunchAgents/`
- the credential folders in the home folder: `~/.aws/`, `~/.gnupg/`, `~/.kube/`, `~/.docker/`,
  `~/.azure/`, `~/.config/gh/`, and `~/.config/gcloud/`. A file in them can also start a
  program, for example `credential_process` in `~/.aws/config`.

`~/.local/bin/`, `<data>/memory/notes/`, and the `.git/` folder of a repository stay writable.

A protected file also covers each name that starts with the file name and a `-`, for example
`<data>/config.yaml-old`.

The check ignores case. For example, `~/.SSH/authorized_keys` and `<data>/SCRIPTS/x.sh` are
refused on every OS.

The protected paths limit the writes. Two tools also refuse to read a protected path, because
they send the bytes to a provider: `view_image` and `transcribe_audio`. The error is
`<path> is a protected path. The tool does not send it to the provider.` `transcribe_audio`
also refuses a text file. The `[image: <path>]` markers of inbound media do not get this check.

### File tools

The file tools resolve the path before the check:

- They resolve `..` and each symbolic link in the path. A link that points to a protected path
  is refused.
- A dangling link counts as its target. A write through the link cannot create the protected
  file.
- They compare the paths after Unicode NFC normalization and case folding. The macOS file
  system ignores case and the normalization form, so another form of a protected path names
  the same file.
- `skill_manage` refuses a write through a symbolic link in `skills/`.

### Shell commands

A shell write is a `>` or `>>` redirection, or one of these commands with the path as an
argument:

- `tee`, `cp`, `mv`, `install`, `ln`, `rm`, `truncate`, `chmod`, `chown`, `dd`, and `rsync`
- `sed -i`, `gsed -i`, and `busybox sed -i`
- `perl -i`, also `perl -pi` and `perl -i.bak`
- `awk -i inplace` and `gawk -i inplace`
- `ed`, `ex`, `sponge`, and `sqlite3`

- The rules match the absolute path. For a path in the home folder, they also match the `~`,
  `$HOME`, and `${HOME}` forms.
- The rules match by prefix. A path that starts with a protected path is also dangerous, for
  example `<data>/.envrc` or `<data>/config.yaml.bak`.
- The rules read the text of the command only. They do not resolve a relative path or a
  symbolic link.

Each command that names `<data>/state.db` is dangerous, also a read. The rule matches the same
path forms as a write rule, and also `state.db-wal`, `state.db-shm`, and `state.db.lock`. Its ID
is `protected_path_use_<slug>`, and its description is `use of the Tether Agent database`. The
rule `sqlite_client_state_db` matches `sqlite3`, `sqlite`, `litecli`, or `sqlite-utils` with the
word `state.db` in the same simple command. It covers `cd ~/.tether && sqlite3 state.db`.

A command that names `state.db` asks each time. It has no `session` or `always` answer, and
the smart judge does not judge it. `state.db` holds the `always` answers and the goal gates,
so one answer must not open it for later commands. Use `session_search` to read the
transcripts. The protected path rules come before the other dangerous rules. So a `session`
answer for `recursive_delete` does not permit `rm -rf build; sqlite3 ~/.tether/state.db x`.

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
- `browser_cookies` with `get` does not return the cookie values. It returns the name, the
  domain, the path, the expiry, the flags, and the length of the value. A tool result goes to the
  model provider and into the transcript, and a cookie value is often a login. A page script,
  also `browser_run_js`, can still read a cookie that is not `HttpOnly`.
- Hide the `browser` toolset from a channel with members, or with users that you do not
  trust. See [Owners and members](#owners-and-members).

## Telegram access

### Allowlists

The bot permits a message when the first of these checks passes:

1. `telegram.group_allowed_chats`, for a group message. `*` permits all groups.
2. `telegram.allow_all_users: true`.
3. The users that pairing approved. `state.db` keeps them.
4. `telegram.allowed_users`, and `telegram.group_allowed_users` for a group message.

When no allowlist is set and `allow_all_users` is `false`, the bot permits nobody.
`group_allowed_users` does not permit direct messages. Use `allow_all_users` only for a test bot.

### Owners and members

An owner is a sender that the allowlists admit by user ID:

- a user that pairing approved
- a user in `telegram.allowed_users`
- in a group, a user in `telegram.group_allowed_users`
- each user, when `telegram.allow_all_users` is `true`

A `*` entry in a list counts, because the owner wrote it. A sender that only
`telegram.group_allowed_chats` admits is a member. A message with no user ID is from a
member. A DM sender that the bot admits is always an owner. The CLI is always the owner.

A member can talk to the bot. A member can run only `/help`, `/new`, `/reset`, `/stop`,
`/steer`, `/now`, `/todo`, `/sessions`, and `/usage`. Each other command, also a new command,
is owner-only. A member gets `⛔ Only the owner can use /<word>.` `<word>` is the name of the
command, for example `reload-mcp`.

A shared session has no user part in its key: a forum topic with
`thread_sessions_per_user: false`, an observed chat, or a group with
`group_sessions_per_user: false`. In a shared session, `/new`, `/reset`, `/stop`, and `/now`
are owner-only for a member. The reply is `⛔ Only the owner can use /<word> in a shared session.`
A plain text of a member in a busy shared session still steers the turn.

- `/usage` from a member shows the `session` form. A range form gets
  `⛔ Only the owner can see the usage of all chats. Send /usage session.`
- `/help` from a member lists the member commands and the skill commands. In a shared
  session, it adds the line `In a shared session, only the owner can use /new, /stop, and /now.`
- A busy reply to a member in a shared session has no **⚡ Do this now** button.
- `/sethome` needs an owner. A member gets `⛔ Only the owner can use /sethome.`
- `/approve` and `/deny` need an owner. They answer only the approvals of the same topic.
- `/start` stays open. A skill slash command is a model turn, and a member can use it.

These rules apply to the answer of a prompt:

- An approval needs an owner. A member gets `⛔ Only the owner can answer this prompt.` Any
  owner can answer each approval.
- A clarify question belongs to the user whose turn asked it. A tap of another user gets
  `⛔ This question is for another user.` A text of another user goes on as a normal message.
- A clarify question from a turn with no user, for example a goal turn, goes to any owner.
- In an observed chat, all users share the session, but the question binds to the sender of
  the turn that asked it. A member answers the question of their own turn. Merged texts or
  left-over texts of more than one sender have no user, so only an owner answers the
  question of such a turn.

A member cannot answer an approval.
So add each trusted group member to `telegram.group_allowed_users`.

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
  Only the **Resume** button of a goal notice starts a model turn, the same as `/goal resume`.
  A notice with no command message encodes the user ID of the session.
- A member tap on a button of an owner-only command gets `⛔ Only the owner can use /<word>.`
  The reply to a member does not show such a button, for example the actions of `/sessions`.
  The `/start` buttons stay.

### Pairing codes

In pair mode, the bot replies with a pairing code. The owner approves the code on the host:

```
tether pairing approve [platform] <code>
```

The default platform is `telegram`.

- A code has 8 characters. The alphabet has no `0`, `O`, `1`, or `I`.
- `state.db` keeps a salted SHA-256 hash of each code, not the code. `pairing list` cannot show
  a code.
- A code expires after 1 hour (`telegram.pairing_code_ttl_s`).
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

1. A protected path (see "Protected paths") is always refused. A folder of
   `gateway.media_allow_dirs` or `TETHER_MEDIA_ALLOW_DIRS` cannot open it. This rule covers the credential folders `~/.ssh/`, `~/.aws/`, `~/.gnupg/`,
   `~/.kube/`, `~/.docker/`, `~/.azure/`, `~/.config/gh/`, and `~/.config/gcloud/`.
2. A file in `<data>/cache/media/`, `<data>/cache/screenshots/`, or a folder of
   `gateway.media_allow_dirs` (or `TETHER_MEDIA_ALLOW_DIRS`) is sent.
3. A file in a denied folder is refused: `/etc`, `/proc`, `/sys`, `/dev`, `/root`, `/boot`,
   `/var/log`, `/var/lib`, `/var/run`, and `~/.ssh`, `~/.aws`, `~/.gnupg`, `~/.kube`,
   `~/.docker`, `~/.config`, `~/.azure`, `~/.gcloud`, `~/Library/Keychains`.
4. In the default mode, all other files are sent.
5. In strict mode (`gateway.media_delivery_strict: true`), another file is sent only when it
   changed in the last 600 seconds. `gateway.media_trust_recent_s` changes the time.
   `gateway.media_trust_recent_files: false` turns this off. A `TETHER_MEDIA_*` variable that is
   set wins over its key.

A refused `MEDIA:` tag shows as `[blocked file: <path>]` in the answer. Use strict mode when users
who are not the owner can talk to the bot. A prompt injection can make the model name any file.

The bot does not send `memory.md` or `user_profile.md`, because they are protected paths.

In the gateway, Tether Agent deletes the files in `<data>/cache/media/` and
`<data>/cache/screenshots/` that are older than `media.cache_retention_days`. The default is
7 days, and `0` keeps all files. The cleanup runs at start and then each 24 hours
(`gateway.housekeeping_interval_s`). `tether chat` alone deletes no files.

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
- The guard scans every file of a skill, whatever its extension, because a script with no
  extension or with an unknown extension can also run. It always scans `SKILL.md`, the known text
  types, each file under `scripts/`, and each file that starts with `#!`. It scans each other file
  that has no NUL byte. It lists the other files as `not scanned (binary)`, and it gives a
  critical finding for an ELF, PE, or Mach-O file.
- The guard also scans a file with a NUL byte when bash can run it: the first line in the first 80
  bytes has no NUL byte. It does not scan such a file when the file is under `.git/`, or when it is
  an image, font, document, archive, audio, video, or model file. Bash deletes a NUL byte after its
  check, so the guard deletes each NUL byte before the scan. A file that the guard reads only by
  this bash rule gets no check for invisible characters. `SKILL.md`, the text types, and the
  files under `scripts/` keep the check, also with a NUL byte. Each scanned file counts to the
  1 MB scan limit.
- The guard does not honor `.skillignore` or `.clawhubignore`. An ignore file in the skill folder
  comes from the author of the skill, so it cannot hide a file from the guard.
- The guard also scans each logical line that a shell builds from lines that end in `\`, `|`, or
  `&&`. After `|` or `&&`, it skips blank lines and comment lines, and it removes a trailing
  comment, as bash does. It does not join a line after `||`, because the next command does not get
  the output. It does not join a line that starts with `|`, because that is a table row. In a
  Markdown file, it joins after `|` or `&&` only inside a fenced code block.

### Installed skills

The owner installs a public skill with `tether skills install <source>`. The loader does not scan
an installed skill again, so the guard scans it once, before the move.

- `tether skills install` and `tether skills update` always scan in strict mode, also when
  `skills.guard` is `false`. The report lists the files that are not text as
  `not scanned (binary)`.
- If the skill has more than 1 MB of text, the command refuses it. No flag passes this limit.
- The trust level comes from the source. A GitHub repo in `TRUSTED_REPOS` (`openai/skills`,
  `anthropics/skills`, `huggingface/skills`, and `NVIDIA/skills`) is `trusted`. Every other
  source, a local folder too, is `community`.

The policy for each verdict and trust level:

| Verdict | community | trusted |
|---|---|---|
| safe | confirm | confirm |
| caution | blocked; `--force` passes it to the confirm | confirm |
| dangerous | blocked; the owner override can pass it | blocked; the owner override can pass it |

- **Owner override:** for a `dangerous` skill, the owner must type the skill name. The owner
  override works only when stdin and stdout are both terminals. No flag gives the owner override.
  `--yes` and `--force` never pass a `dangerous` verdict. The lockfile records the owner override.
- **Hardline rule:** the approval gate blocks `tether skills install`, `tether skills update`, and
  `tether skills uninstall` from the agent in every approval mode. No cron setting and no "Always"
  answer allows them. Only the owner runs these commands, in a terminal. `tether skills check` and
  `tether skills list` are read-only, and the agent can run them.
- A command that hides the words, for example through a shell variable, can get past the hardline
  rule. The terminal tool runs commands with no TTY, so the owner override is a second layer.
- The lockfile is `skills/.hub/lock.json`. It records the source, the commit, the trust level,
  the verdict, and a content hash for each installed skill.

### API keys in skills

Do not put an API call with a key in a skill. Use this pattern:

1. Put the API call in a script under `~/.tether/scripts/`.
2. Make the skill tell the agent to run that script with `terminal`.
3. Add the name of the key to `terminal.env_passthrough`.

The `required_environment_variables` of a skill do not pass a key. `tether skills install` and
`tether skills update` list these names.

The guard scans skill folders only. It does not scan `scripts/`.

## Cron scripts

A cron script job runs a script with no model call and with no approval. These limits apply:

- The script must be in `~/.tether/scripts/`. A relative path resolves there. The
  program refuses a path or a symbolic link that goes outside that folder.
- `write_file` and `patch` refuse to write in `scripts/`. A shell write to `scripts/` is
  dangerous, so the approval gate asks the user. In a cron run, `approvals.cron_mode` sets
  the result.
- The script gets the child environment with no secrets.
- The script stops after `cron.script_timeout_s` seconds (default 120). The program then stops the full process group.
- A failure or a timeout always sends an alert with the tail of stderr.

## MCP servers

The approval gate does not check MCP tools, because Tether Agent cannot see what an MCP server
runs. An MCP tool can do anything that its server can do.

- Enable only the MCP servers that you trust.
- Use the toolsets to hide MCP tools from a channel. Each server has the toolset
  `mcp_<server>`, and the toolset `mcp` has all MCP tools.
- A stdio server gets the child environment with no secrets, plus its own `env` block.
- A configured header goes only to a trusted origin: the origin of the server URL and, for an
  SSE server, the origin of the `endpoint` URL that the server sends. An origin is the scheme,
  the host in lower case, and the port. A missing port is 80 for `http` and 443 for `https`.
  A redirect to another origin gets no configured header, and the first such request logs a
  warning that names the origin.
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
- At start, Tether Agent sets mode 0700 on a data folder with group or other permissions. It
  sets mode 0600 on `state.db`, `state.db-wal`, and `state.db-shm` in the same case. Each
  change logs a warning with the old mode. `tether doctor` warns about such a data folder.

The systemd unit of `tether setup` has no containment settings. You can add a drop-in file:
`/etc/systemd/system/tether.service.d/hardening.conf` for a system unit, or
`~/.config/systemd/user/tether.service.d/hardening.conf` for a user unit.

```ini
[Service]
UMask=0077
ProtectHome=read-only
ReadWritePaths=/home/<user>/.tether
```

Then run `systemctl daemon-reload` and `systemctl restart tether`. Add `--user` for a user
unit. Know the cost before you add it:

- `UMask=0077` gives mode 0600 to each file that the agent makes for you. Another user or a
  web server cannot read such a file, for example a file in a web root.
- `ProtectHome=read-only` stops each write in the home folders. The agent cannot edit a
  repository or install a tool in your home folder. `ReadWritePaths` keeps the data folder
  writable. Write the real path of your data folder.

### execute_code

`execute_code` runs Python code on the host. Before the script runs, the approval gate does
these checks:

1. The hardline list, the sudo guard, and the gateway self-stop check run on the full text of
   the script. A match blocks the script.
2. With `approvals.mode: off`, the script runs.
3. The dangerous rules run on the text. A literal match, for example `rm -rf` in a
   `subprocess` string or a protected path, makes the script ask each time.
4. A script with no literal match runs when the session has a `session` answer for
   `execute_code`. The saved `always` answers do not apply.
5. In smart mode, the judge gets a script prompt and at most the first 8000 characters
   (`approvals.smart_script_max_chars`). A low-risk answer runs this script only. A longer script goes to the user.
6. Else the gate asks the user.

The prompt uses the rule `execute_code` with the description `run a Python script on the host`.
A literal match adds its description, for example
`run a Python script on the host (recursive delete)`. The answers are `once`, `session`, and
`deny`. A `session` answer covers the later scripts of the session with no literal match.

In a cron run, a script with no literal match runs. A script with a literal match follows
`approvals.cron_mode`.

The gate cannot see a command that the script builds while it runs, for example from parts of
a string or from decoded data. A Python file that `terminal` runs gets only the shell rules.
So `write_file x.py` and then `python3 x.py` pass the gate. This is a known limit. The tool
stubs send their calls through the approval gate and the protected path checks. Hide the
`code_execution` toolset from a channel when you do not trust its users.

## Self-update

`tether update` replaces the binary with a release from GitHub.

- The command downloads the archive from the GitHub release of the repository over HTTPS.
- It checks the SHA-256 of the archive against `checksums.txt` from the same release.
- It runs the new binary one time with `version` and compares the output.
- It replaces the file with one rename in the folder of the binary.
- It reads `config.yaml` and `.env` only for the `update.*` time limits. A file that does not load
  gives a warning and the defaults. It never reads `state.db`.
- It needs write permission on the folder of the binary. A binary in `/usr/local/bin` needs
  `sudo tether update`.
- A running gateway keeps the old code until a restart. In a terminal, the command asks
  `Restart the gateway now?`. If you answer yes, it runs `systemctl restart` for the unit. It
  runs a system unit only as root. Without a terminal, with `--no-restart`, or when it cannot
  restart the unit, it prints the restart command.
- The checksum file comes from the same release. The check finds a cut-off or a corrupted
  download. It does not protect against a compromised GitHub account.
