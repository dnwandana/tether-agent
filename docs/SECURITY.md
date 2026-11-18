# Security model

This document tells how Tether Agent controls what the agent can do. It also gives the known
limits. Read the limits before you give the agent access to a machine that holds important data.

[CONFIG.md](CONFIG.md) gives the default of each key that this document names.

## Summary of the limits

- Commands run on the host, as the user that runs Tether Agent. There is no container and no
  sandbox.
- The approval gate checks shell commands, `execute_code` scripts, MCP tool calls that are not
  reads, and the permission requests of a coding agent (`acp_delegate`). It cannot see what an
  MCP server does with a call, so it uses the class of the tool name. See
  [MCP tool classes](#mcp-tool-classes).
- In manual mode, each new `execute_code` script asks. A ✅ Session answer covers only the same
  script bytes. A script with a literal dangerous command or a protected path asks each time.
  The gate cannot find a command that the script builds while it runs. In a cron run,
  the gate blocks each `execute_code` script unless `approvals.cron_mode` is `approve`.
- A human-only action needs the owner to tap ✅ Once each time. No mode, grant, or judge lowers
  this level. See [Human-only actions](#human-only-actions).
- A command that the gate cannot read asks each time, with no grant and no judge. See
  [Commands that the gate cannot read](#commands-that-the-gate-cannot-read).
- `/mode` sets the approval mode of one chat or topic: `read-only`, `guarded`, or `full`. See
  [Modes for a chat or a topic](#modes-for-a-chat-or-a-topic).
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
- In a chat of `telegram.group_allowed_chats`, each member can talk to the bot.
  A member gets only the toolsets of `toolsets.member` (default `[web, ask, todo]`).
  Only an owner can answer an approval and run the owner commands. See
  [Owners and members](#owners-and-members).
- A coding agent of `acp_delegate` runs on the host as the same user. Its permission
  preset sets which actions it asks for. Tether Agent checks the paths and the commands of the
  requests that the coding agent sends. The permission preset decides which requests come.
  A hardline command of the coding agent is refused with no prompt.

## Approval gate for commands

The approval gate checks every `terminal` call. It also checks each `execute_code` script (see
[execute_code](#execute_code)), and each tool call of the script goes through the same checks as a
model call. The gate is a port of the Hermes approval flow.

### The order of the checks

1. **Normalize.** The gate applies Unicode NFKC, removes ANSI escape sequences and zero-width
   characters, and changes the command to lower case. Hidden characters then cannot get past
   the rules.
2. **Read the command.** The gate reads the command as bash does. It finds each simple command,
   also the commands inside wrappers such as `sudo` and `bash -c`. See
   [Shell commands](#shell-commands) and [Wrappers](#wrappers).
3. **Hardline list.** These commands are always blocked. No approval mode, cron setting, or
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

   The hardline rules read the raw text and the rule line of each simple command.
4. **Sudo guard.** The gate blocks `sudo -S` when no sudo password is set, because the model
   then guesses the password. Set `SUDO_PASSWORD` in `.env` if the agent must use sudo.
5. **Gateway self-stop.** In the gateway, the gate blocks a command that stops or restarts the
   gateway process itself. Examples are `systemctl restart tether`, `tether gateway stop`,
   `pkill tether`, and `tether update --restart`. The stop would cut the turn that runs the
   command. A cron run does not get this check.
6. **Human-only rules.** A command that matches a human-only rule needs the owner to tap
   ✅ Once each time. No mode, grant, or judge lowers this level. See
   [Human-only actions](#human-only-actions).
7. **Dangerous rules.** The `state.db` rules come first. Then come the other protected path
   rules, then the 56 Hermes dangerous rules. A command that matches one of
   them needs an approval. Examples are `rm -r`, `chmod 777`, `curl … | sh`, and
   `git push --force`. A shell write to a protected path and each command that names
   `state.db` are also dangerous. See [Shell commands](#shell-commands). The protected path rules
   read the raw text, its second form, and the rule line of each simple command. The other rules
   read the rule line of each simple command and the command view. The gate looks at all
   the rules that match. The command runs with no question only when each matched protected rule
   has a grant and the first general rule has a grant. Else the gate asks with the first rule
   that has no grant. A grant for a protected path does not cover another dangerous rule in
   the same command.
8. **No match.** When the gate read the command and no rule matches, the command runs. The gate
   writes no log row.
9. **Could not read.** When the gate could not read the command, it asks with the rule
   `could_not_read`, or with the first dangerous rule that matches the raw text. There is no
   grant and no judge. See
   [Commands that the gate cannot read](#commands-that-the-gate-cannot-read).
10. **Live mode.** The mode of the topic (`/mode`) applies at this time. The topic mode `full`
    counts as `approvals.mode: off`. Any other topic mode follows `approvals.mode`. With `off`,
    the command runs, also a command that the gate could not read.
11. **Grants.** The command runs with no question when an earlier answer covers it. See
    [The question and the scopes](#the-question-and-the-scopes).
12. **Cron.** In a cron run, `approvals.cron_mode` decides. See [Cron runs](#cron-runs).
13. **Smart judge.** In smart mode, the judge reviews the command first. See
    [Approval modes](#approval-modes).
14. **Ask.** The gate asks the owner.
15. **Check again.** After a tap that permits the command, the gate reads the mode of the topic
    again. When the mode is now `read-only`, the command does not run. The model gets "The mode
    of this topic changed to read-only while the approval waited." The gate saves no grant.

### Human-only actions

A human-only action needs the owner to tap ✅ Once each time. The owner can also tap ❌ Deny. The
level has these rules:

- There is no ✅ Session answer, no ✅ Always answer, no grant, and no smart judge.
- `approvals.mode: off`, `/mode full`, and `approvals.cron_mode: approve` do not lower the level.
- The gate blocks the action in a cron run, in a member turn, and when no Prompter is present.
  The model gets this text:
  `BLOCKED (human-only): <description>. This action needs the owner to tap Approve each time, and no owner can answer here.`
  The text ends with the sentence of the block tail. See [Wrappers](#wrappers).
- `/approve all` leaves a human-only prompt open. The owner must tap the prompt.
- A goal gate that is human-only is refused when the owner adds it. A saved goal gate that is now
  refused pauses its goal. See [Goal gates](#goal-gates).

These shell rules are built in:

| Rule ID | Description | Examples |
|---|---|---|
| `human_only_password_change` | password change | `passwd`, `chpasswd`, `smbpasswd`, `htpasswd` (not `-c`), `usermod -p` |
| `human_only_account_creation` | account creation | `useradd`, `adduser`, `usermod -aG sudo`, `gpasswd -a <user> docker` |
| `human_only_credential_access` | new credential access | `ssh-copy-id`, a write to `authorized_keys`, `gh auth login`, `gcloud auth login`, `aws configure`, `az login`, `docker login`, `npm login` |
| `human_only_permanent_deletion` | permanent deletion | `shred`, `wipefs`, `srm`, `blkdiscard`, `dd if=/dev/zero of=<file>` |

A readable command is matched by the program word and the arguments of each simple command, also
inside a wrapper. A command that the gate cannot read is matched by the raw text. An
`execute_code` script is matched when a string literal of the script starts with one of these
programs, for example `subprocess.run("useradd bob")`.

MCP tools get the class `human-only` when the tool name has one of these word sets. See
[MCP tool classes](#mcp-tool-classes).

- a payment word with a payment noun, for example `create_payment` or `transfer_funds`
- an account verb with an account noun, for example `invite_member`
- a credential verb with a credential noun, for example `rotate_token`
- a password change, for example `reset_password`
- a purge word, for example `purge_cache` or `permanently_delete`
- `empty` with `trash`, and a delete verb with `all`, `bulk`, `batch`, or `many`

`approvals.human_only` can raise more rules. An entry is the ID of a dangerous rule, for example
`git_force_push`, or an MCP tool as `mcp:<server>.<tool>`. The list can only raise a rule. It
cannot lower a built-in rule. The start stops with an error when an entry is not the ID of a
dangerous rule, or when it is the ID of a hardline rule, because a hardline rule is already
blocked. `tether doctor` shows the same error.

### Commands that the gate cannot read

The gate must read a command before it can match the rules on its parts. The gate cannot read a
command in these cases:

- The command is longer than 32 KB.
- The command has a syntax error.
- The command nests more than 40 levels of substitutions or subshells.
- The command starts more than 4 new parses, for example `bash -c` inside `bash -c`.
- The command has more than 16 wrappers.
- A program name is built at run time, for example `$CMD`, or has a glob, for example
  `/usr/sbin/rebo?t`.
- A program name is empty.
- A wrapper has an option that the wrapper table does not know.
- The command expands to more than 256 words through brace expansion, or one word has more than
  256 braces.
- `su` or `runuser` gets words after the user name with no `-c`. The shell of the user then runs a
  script file.

Such a command gets the synthetic rule `could_not_read`. When a dangerous rule also matches the
raw text, the question names that rule and tells that the gate could not read the full command.

- The command asks each time. There is no ✅ Session answer, no ✅ Always answer, no grant, and
  no smart judge.
- `approvals.mode: off` and `/mode full` run the command.
- A cron run blocks the command, also with `approvals.cron_mode: approve`.
- The text for the model adds "Use write_file for large content." A long command is the most
  common cause.

### Wrappers

A wrapper is a program that runs another command. The gate reads through a wrapper to the inner
command and applies the rules to it. The table has three kinds:

| Kind | Programs | Grant key |
|---|---|---|
| Transparent: runs the command as the same user on the same host | `env`, `eval`, `nice`, `nohup`, `timeout`, `time`, `exec`, `command`, `builtin`, `busybox`, `setsid`, `stdbuf`, `ionice`, `taskset`, `chrt`, `flock`, `xargs`, `watch`, `strace`, `unshare`, `script`, and the shells `sh`, `bash`, `zsh`, `dash`, `ksh`, `ash`, `fish`, `csh`, `tcsh` | The rule ID |
| Privilege: runs the command as another user | `sudo`, `doas`, `pkexec`, `su`, `runuser` | `<rule> via <wrappers>` |
| Other host or container | `ssh <host>`, `docker exec`, `podman exec`, `kubectl exec`, `chroot`, `nsenter` | `<rule> via <wrappers>` |

These rules apply:

- A wrapper form that only reports runs no inner command. Examples are `sudo -l`,
  `command -v`, `ionice -p`, `taskset -p`, and `chrt -p`.
- Each wrapper fails the read on an option that the table does not know. The command then
  counts as a command that the gate cannot read. The block text names the wrapper and the
  option, for example `the command has an option that the gate does not know: xargs --foo`.
- The reader knows the forms where an option comes after the target: `flock <file> -c <cmd>`,
  `ssh <host> -t <cmd>`, `su <user> -c <cmd>`, `kubectl exec <pod> -c <container> -- <cmd>`, and
  `script <file> -c <cmd>`.
- `sudo NAME=value <cmd>` reads the inner command after the assignments.
- `watch` runs its words through `sh -c`, so the gate reads them as a new parse.
- The `ssh` options `ProxyCommand`, `LocalCommand`, and `KnownHostsCommand` run a local command.
  The gate reads that command too.
- The reader makes at most 4 new parses for one command, and reads at most 16 wrappers. A deeper
  command cannot be read.
- A ✅ Session or ✅ Always answer for `rm -r x` does not cover `sudo rm -r x`. The grant key is
  `<rule> via <wrappers>`, with the wrapper text, for example `recursive_delete via sudo` or
  `recursive_delete via ssh prod`.
- The question shows the flagged line with the wrapper, for example
  `recursive delete (inside sudo)`.
- The block text of the gate ends with this sentence: "Do not retry it with a different
  spelling or another tool. Tell the owner what you tried to run."

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
  judge again. A command that matches a protected path rule never goes to the judge, and the
  gate asks the user. This includes each command that names `state.db`. An `execute_code`
  script with a literal match of a protected path rule also skips the judge. For an
  `execute_code` script, the judge gets a script prompt and at most the first 8000
  characters (`approvals.smart_script_max_chars`). A longer script goes to the user.
- `off`: the command runs with no question. The hardline list, the sudo guard, and the human-only
  rules still apply.

`approvals.mode` is the default for the whole process. The `/mode` of a chat or a topic wins over
it in that topic. See [Modes for a chat or a topic](#modes-for-a-chat-or-a-topic). The topic mode
`full` counts as `off`. The topic mode `guarded` and an unknown topic mode follow `approvals.mode`.

### Modes for a chat or a topic

The owner sends `/mode` to set the approval mode of the chat or the topic where the owner writes.
The command is owner-only. It works while a turn runs. These values are possible:

| Mode | Result |
|---|---|
| `read-only` | The agent reads, searches, and answers. It cannot write files, run commands, or change memory, skills, or cron jobs. |
| `guarded` (default) | The gate follows `approvals.mode`. |
| `full` | The gate asks no question for a dangerous command. The hardline rules, the sudo guard, and the human-only rules still apply. |

These rules apply:

- `/mode full` asks for a confirm. In Telegram, the owner taps **⚡ Confirm full**. In the CLI,
  the question is `[y/N]`. A typed `/mode full confirm` also confirms.
- `/mode read-only` and `/mode guarded` apply at once. `/mode` with no argument shows the
  current mode.
- The `chat_modes` table of `state.db` keeps the mode. A `guarded` mode has no row. The mode
  survives `/new` and a restart. A move of a group to a supergroup moves the row.
- The scope is `cli` for the CLI, or `telegram:<chat id>:<thread id>` for Telegram. The thread id
  is `0` outside a forum topic. All users of a topic share one scope.
- A cron run has no mode. The topic mode of the chat that made the job does not apply. Only the
  log row of the run gets the scope of that chat.
- A mode that the gate cannot read counts as `guarded` for the gate, and as `read-only` for the
  tool check. So a database error never gives `full`.
- In `read-only`, these tools run: `read_file`, `web_search`, `web_fetch`, `view_image`,
  `session_search`, `skill_view`, `skills_list`, `clarify`, `todo_list`, `browser_navigate`,
  `browser_read`, `browser_take_screenshot`, `transcribe_audio`, `text_to_speech`,
  `generate_image`, and `delegate_task`. The actions `read` and `search` of `memory`, `list` and
  `runs` of `cronjob`, and `get` of `browser_cookies` also run. An MCP tool runs when its class
  is `read`.
- In `read-only`, the gate refuses these tools: `terminal`, `execute_code`, `write_file`,
  `patch`, `skill_manage`, `acp_delegate`, `browser_click`, `browser_fill`, and
  `browser_run_js`. It refuses each other tool and each other action that is not in the list
  above. A read-only turn does not send these tools and the MCP tools that are not `read` to
  the model. The system prompt of the turn has no notes for them. A child agent of the turn
  gets the same list.
- A refused call gives the model this text:
  `This topic is read-only. The tool <name> cannot run here. Tell the owner, who can send /mode guarded.`
- A child agent of `delegate_task` copies the mode of its parent. A child of a `read-only` parent
  can read only.
- A read-only topic starts no background review.
- Goal gates still run in a read-only topic. They have their own checks. See
  [Goal gates](#goal-gates).

### Cron runs

No user is present in a cron run. `approvals.cron_mode` sets the result for a dangerous command
in a cron run:

- `deny` (the default): the gate refuses the command with no question.
- `approve`: the command runs.

The config accepts only these two values. Only `approve` lets a dangerous command run.

Two cases apply before `approvals.cron_mode`:

- When `approvals.mode` is `off`, the command runs.
- When each matched rule has a grant, the command runs. See
  [The question and the scopes](#the-question-and-the-scopes). A command that names `state.db`
  never has a grant.

`approvals.cron_mode` does not lower these cases:

- A human-only command is blocked. See [Human-only actions](#human-only-actions).
- A command that the gate cannot read is blocked, also with `approve`. The model gets
  `BLOCKED: <reason>. A cron job has no user present to approve a command that the gate cannot read, also with approvals.cron_mode: approve.`
- An MCP `write` or `send` call under `deny` is blocked. The model gets
  `BLOCKED: <class> call <server>.<tool> in a cron run. Set approvals.cron_mode: approve to allow MCP write and send calls in cron jobs.`

In a cron run, each `execute_code` script follows `approvals.cron_mode`, also a script with no
literal dangerous command. The text of a script does not show what the script does. With
`deny`, the gate blocks the script. A script with no literal match gets this text:

`BLOCKED: execute_code runs Python on the host, and cron jobs run without a user present to approve it. Ask the user to move the code to a cron script in <data>/scripts/, or to set approvals.cron_mode: approve in config.yaml.`

A script with a literal match gets the block text that names the rule. With `approve`, the
script runs. The hardline list and the sudo guard still apply. A cron script job does not
change. See [Cron scripts](#cron-scripts). Remove `code_execution` from `toolsets.cron` for
full control.

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
- `session`: permits the same rule for the rest of the session. The grant key of a command is
  the rule ID. A command behind a privilege or host wrapper has its own key, for example
  `recursive_delete via sudo`. See [Wrappers](#wrappers). For an `execute_code` script, the key
  is the SHA-256 hash of the script, so the answer covers only the same script bytes.
- `always`: permits the same rule for the same command from now on. The row binds the rule, the
  command bytes (with the outer white space trimmed), the workdir, the real path of each
  program, and the wrapper text. The program saves the row in the `approval_grants` table of
  `state.db`. A change of one value, for example a new workdir, needs a new answer. The running
  agent never writes its own `config.yaml`.
- `deny`: refuses the command. The model gets the result `BLOCKED by user: <pattern name>`.

A command has no `always` answer in these cases:

- The rule is a protected path rule.
- The request is from a member turn.
- The gate could not read the command.
- A program did not resolve to a real file in the `PATH` of the command.
- The command has text that the shell builds at run time, for example `$VAR` or `$(cmd)`.
- A variable assignment comes before the program word, for example `LD_PRELOAD=x prog`.
- A relative program comes after a `cd`.
- The command changes `PATH` or `BASH_CMDS`, or runs `hash` or `enable`. Then the program that
  runs can differ from the program that the gate resolved.
- The request has no workdir.

A command with a credential in it gets only ✅ Once and ❌ Deny. A credential is a part that the
redactor masks, for example `--password x` or `https://user:pass@host`. A variable such as
`$TOKEN` or `"$TOKEN"` is not a credential, because the command holds only the name of the
variable. A `--password` or `--token` flag in the text of an `echo` or a `printf` is not a
credential. The redactor still masks it in the logs.

If the grant store is damaged, the gate turns off all saved answers until the next restart. A
missing table, a missing column, or a value that is not valid gives this state. The gate then asks
each time, shows only ✅ Once and ❌ Deny, and saves nothing. `/approvals` shows this line:
`Saved answers are off, because the answer store is damaged: <cause>. Restart the gateway after you repair it.`
`tether doctor` gives a warning.

When Tether Agent updated to this model, it removed the old Always rows. Such a row covered a
rule for each command. The first `/approvals` and `tether doctor` show the count, for example
`2 old Always answers were removed by the update. The gate asks again once for each command.` The
gate then asks one time for each command, and the new row binds that command.

`tether chat` and the systemd gateway can have a different `PATH`. The programs resolve to the
real files by `PATH`, so an Always row of one process can fail to match in the other. The gate
then asks again.

These rules limit the scopes:

- An answer covers only the turns of an owner.
  A member turn reads no `session` answer and no `always` answer, also in a shared session.
  Its prompt shows only `once` and `deny`. So an owner's answer to a member prompt does not
  cover a later turn. This also holds for `execute_code`.
- The smart judge still runs for a member turn. A judge approval covers that one command only.
- A protected path rule has no `always` answer. A typed `/approve always` gives `session`.
  A protected path rule reads only the `session` grants. It never reads `approval_grants`, so
  an `always` row that an older version saved does not permit it.
- A grant covers one rule. When a command matches more than one rule, the gate asks for the
  first rule that has no grant.
- A command that names `state.db` has no `session` and no `always` answer. It asks each time.
  A saved `always` answer does not apply to it.
- A human-only request has only `once` and `deny`.
- An `execute_code` script has no `always` answer. In manual mode, each new script asks.
- Only an owner can answer an approval. See [Owners and members](#owners-and-members).

A permission request of a coding agent has no `always` answer. Some requests also have
no `session` answer. See [Coding agents (ACP)](#coding-agents-acp).

The owner sends `/approvals` to see the saved `always` answers. The command is owner-only.
Each row shows a number, the description of the rule, the date, and an 8-character code. The
code comes from the row id.
The list leaves out the rows of protected path rules, because the gate never reads them.
With no rows, the list part of the reply is
`No Always answers. The gate asks for each dangerous command.`

- A row of a command has a second line. It shows the redacted preview of the command on one line,
  cut at 60 characters, and the workdir, with `~` for the home folder. A group chat shows no
  preview and no workdir, because each member can read the reply.
- A row of an MCP tool shows `MCP write · <server> · <tool>`.
- The list shows the newest 20 rows. A last line gives the number of the other rows.
- In Telegram, a **Remove N** button removes an answer. The list message then shows the result
  and the rows that are left, with new buttons. The reply has at most 20 **Remove N** buttons.
- In Telegram, with 2 or more rows, a **🗑 Remove all** button removes all answers in the list.
  The bot asks first. **Cancel** shows the list again. **🗑 Remove all** removes all answers in
  the list.
- In the CLI, send `/approvals remove <number> <code>` to remove one answer. Send
  `/approvals remove all <list code>` to remove all answers in the list. With 2 or more rows,
  the last line of the list gives this command with the list code. The CLI asks with `[y/N]`
  first.
- When the list changed after the reply, the remove removes nothing and shows the current
  list. If the answer is already gone, for example after a double tap, the reply starts with
  `ℹ️ This answer is already removed.` After **🗑 Remove all** on an empty list, it starts with
  `ℹ️ All answers are already removed.` In other cases, it starts with
  `⚠️ The list changed. Nothing was removed.` A remove with a missing word gets
  `The list changed. Send /approvals to see it again.`
- A remove deletes the `always` row and the `session` answers of that rule in the same
  process. The gate then asks again.
- The CLI and the gateway each keep their own `session` answers. A remove in one process
  does not clear a `session` answer in the other process. That answer ends with its session.
- `tether doctor` shows the number of `always` answers, for example
  `approvals: 2 Always answers (send /approvals to see them)`.

### Approval log

The gate writes one row to the `approval_log` table of `state.db` for each decision on a request
that matched a rule, a request with an MCP class other than `read`, and each block of the floor.
A row holds the time, the session, the scope, the kind of run, the tool, the rule, the MCP
server, tool, and class, the decision, the source, the answer, the SHA-256 hash of the command,
and a preview.

- The decision is `allowed`, `denied`, or `blocked`.
- The source tells why. The values are `owner`, `grant`, `mode-off`, `mode-full`, `cron-mode`,
  `smart`, `timeout`, `stopped`, `prompt-error`, `hardline`, `human-only`, `could-not-read`,
  `member`, `no-prompter`, and `mode-read-only`.
- The preview is one line. The redactor masks it, and it has at most 300 characters. A password
  change keeps only the program word.
- A command with no match and a `read` MCP call write no row.
- A failed write to the log never changes a decision. The gate writes a log line.
- A `/stop` during a decision still writes its row, with the source `stopped`.
- A cron row has the scope of the chat that made the job. A job with no chat has no scope.
- `/delete` of a session keeps its rows.
- `approvals.log_retention_days` (default 180) sets the age at which the gateway deletes a row.
  The value `0` keeps all rows. The gateway deletes the old rows in its housekeeping.

The owner sends `/approvals log` to see the last 15 decisions of this topic. The owner sends
`/approvals log all` to see the last 15 decisions of all chats. `/approvals log all` works only in
the CLI and in a private chat, because a group can read the reply. Each row shows the time, the
decision, the source, the answer, the rule, and a preview of at most 60 characters.

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
3. **Human-only rules and unreadable commands.** A human-only command is refused with
   `BLOCKED (human-only): <description>. This command cannot be a gate.` A command that matches
   a rule of `approvals.human_only` is refused in the same way. A command that the gate cannot
   read is refused with `BLOCKED: could not read the command: <reason>. This command cannot be a gate.`
4. **Dangerous rules.** A command that matches a dangerous rule needs a confirmation. An
   example is `sudo -S`, which matches the rule "sudo with privilege flag". Telegram shows the
   **Add gate** and **Cancel** buttons. The buttons stop working after `approvals.timeout_s`,
   and then the gate is not added. The CLI asks with `[y/N]`. The add check does not read
   `approvals.mode`.
5. **No match.** The gate is added at once.

Each add writes a row to the approval log, with the tool `goal_gate`. See
[Approval log](#approval-log).

After the add check, these rules apply:

- The gate runs before each judge call, with no approval prompt.
- Before each run, Tether Agent checks the gate again with the hardline list, the gateway
  self-stop check, the sudo guard, the human-only rules, and the read of the command. A refused
  gate does not run. The goal pauses at once with the reason, with no retries and no
  continuation turns. A saved gate that the update made human-only pauses its goal in this way.
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

- **Floor.** A command of the hardline list is refused with no prompt. The sudo guard and the
  gateway self-stop check also apply. The sudo guard refuses `sudo -S` also when
  `SUDO_PASSWORD` is set, because the adapter does not get the password. Tether Agent selects
  a reject option of the agent. The progress line shows `hardline: <description>`, `sudo -S`,
  or `gateway self-stop`, and the log gets a warning. An earlier `session` answer cannot
  permit the command.
- **Dangerous rules.** A command that matches a dangerous rule asks each time and has no
  `session` answer. The message names the rule, for example
  `claude wants to run a command (dangerous: recursive delete)`.

The command text is the `command` field of the request. Tether Agent joins a list of words
with spaces.

Tether Agent also checks the paths of each request, whatever its kind. It reads the
locations, the diffs, and the `file_path` and `notebook_path` fields of the raw input. It
expands a leading `~`, joins a relative path to the work folder, and resolves `..` and the
symbolic links.

- **A protected path.** The request is refused with no prompt. The progress line shows
  `blocked: protected path <path>`, and the log gets a warning. This is the rule of
  `write_file`. See [Protected paths](#protected-paths).
- **A path outside the work folder.** The message gets the note
  `(outside the work folder: <path>)`. The request asks each time and has no `session`
  answer. A link in the work folder to a file outside it counts as outside.
- **An edit, a delete, or a move with no path.** The message gets the note
  `(the request names no path)`. The request asks each time and has no `session` answer.

The checks apply only to the requests that the coding agent sends.
The permission preset decides which requests come.
With `auto` (the default) and `risky`, Claude Code applies most edits with no request, so the
checks do not run for them.

- The answers are `once`, `session`, and `deny`. There is no `always` answer. Tether Agent
  never selects an "always" option of the agent, so the agent does not write a durable
  rule into its own settings.
- `session` permits the same kind of action in the same ACP session of the coding agent.
  For an edit, a read, a fetch, or another kind that is not a command, it permits all
  requests of that kind with paths only inside the work folder. For a command, the rule also holds the program name. So `session`
  on `go test ./...` also permits `go build`, but not `rm`. `session` on `rm notes.txt` also
  permits `rm old.txt`. A later `rm -r build` asks, and `rm -rf ~` is refused.
- These requests have no `session` answer. They ask each time.
  - A compound command: a command with `&`, `|`, `;`, `$(`, a backtick, `<`, `>`, or a new
    line. This includes `&&`, `||`, redirects, and process substitutions.
  - A wrapper program. The list is the shared wrapper table of the approval gate, for example
    `sudo`, `env`, `bash`, `xargs`, `timeout`, `ssh`, `docker`, or `nsenter`. See
    [Wrappers](#wrappers).
  - A command that the gate cannot read, and a command that has an inner command, for example
    `bash -c '…'`.
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
- Some agents send a request with no allow-once option or no reject-once option. Tether
  Agent then asks with the options of the agent. The question shows the description, the
  notes, and the command or the diff in a code fence. The body is cut to 1000 characters, and
  a hidden character shows as a mark. The text of the request cannot close the fence.
- In a member turn, a request reads no earlier `session` answer, and the message does not
  offer a `session` answer. A request with no allow-once or reject-once option is refused,
  because that question goes to the user of the turn, not to an owner.

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
empty allowlist refuses each call. An edit request for a path outside the folder asks each
time and has no `session` answer. The agent can apply an edit with no request when its preset
permits it. Tether Agent cannot check such an edit.

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
`toolsets.member` is not a channel. It limits the turns of a member.

- The registry removes the hidden tools before it sends the request. The model never sees a
  hidden tool.
- A call to a hidden tool, for example from an old message, gives an error result.
- A cron job and a child run can only make the toolsets of their channel smaller.
- A cron run cannot use `cronjob` or `clarify`.
- `/mode read-only` hides the tools that change things in one chat or topic. The toolsets stay
  the same. See [Modes for a chat or a topic](#modes-for-a-chat-or-a-topic).

There is one exception. The background review fork gets the same tool schemas as its parent, so
the prompt cache hits. The dispatcher permits only the `memory`, `skill_view`, and
`skill_manage` tools in the fork. A call to another tool gives an error result.

A member turn is a turn of a member. See [Owners and members](#owners-and-members). These
rules apply to it:

- A member turn gets the toolsets of its channel that `toolsets.member` also names. The
  default is `[web, ask, todo]`. An empty list gives no tools. `all` gives the channel set.
- A member never gets `cronjob` or `delegate_task`, also with `all`. A cron job runs as an
  owner turn with the tools of `toolsets.cron`. `acp_delegate` follows the list.
- In a member session, the model sees only the member tools.
- In a shared session, the model sees the tools of the channel. Thus
  the tool schemas stay the same bytes for each turn, and the prompt cache hits. A member turn
  can call only the member tools. A call to another tool gives the result
  `This tool is for the owner only. A member can use these tools: <names>.`
- An `execute_code` script and a `delegate_task` child of a member turn also get only the
  member tools.
- An unknown name in `toolsets.member` stops the start with
  `config: toolsets.member: unknown toolset "<name>"`.
- A human-only request of a member turn blocks. A member cannot answer it, and no owner is
  asked. See [Human-only actions](#human-only-actions).

## Memory and background review

`memory.md` and `user_profile.md` go into the system prompt of each later session. A skill
description goes into the skills index of the prompt. So text that the agent saves there stays
active after `/new`, after a restart, and after a model change.

- The background review reads the full conversation, also tool results (for example web pages and
  command output) and group messages that were not sent to the agent. The review prompt tells the
  model that this text is data, and that it must not save an instruction from it.
- A member turn starts no background review: not after the turn, not on a compaction, and
  not on `/new` or `/reset` in a member session. The review of an owner turn in a shared
  session still reads the member messages of that session.
- The prompt of a member session has no MEMORY section, no USER PROFILE section, and no note
  topic list. Its tool notes and its skills index show only the member tools. A shared session
  keeps the owner memory in its prompt. Thus
  each member of a shared session can read the owner memory through the agent.
- The `memory` tool scans the text of each `add` and `replace` with the memory threat table.
  The table has 25 rules of Hermes `tools/threat_patterns.py`, 4 credential rules of the skill
  guard, and a check for invisible characters and Unicode tag characters. Any finding refuses
  the write. A `remove` is never refused. A text over the limit of 4000 characters for one entry
  (`memory.entry_max_chars`) gets the length error, with no scan.
  Before the rules, the scan applies Unicode NFKC and folds lookalike letters, for example a
  Cyrillic І, to the Latin letter. The command gate uses the same fold table.
- Known limit: the scan finds known phrases. A new wording of the same instruction passes.
  For members, the control is the member toolsets and the review rule.
- The topic list of the prompt leaves out each note topic whose name,
  with each `-` read as a space, has a finding of the memory threat scan.
  The agent logs a warning for it. The note file stays.
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
- A Telegram chat can list, change, pause, run, and remove only the cron jobs that it made, and
  read only their runs. A job of another chat gives the same error as a job that does not exist.
- From a Telegram chat, `deliver` can be `origin`, `local`, or `telegram:<chat_id>` of the same
  chat. The CLI can deliver to any chat.
- A cron run gets the scope of the chat that made its job.

The memory files and the skills are one set for all users. By default, a member cannot use
`memory`, `skills`, or `cronjob`, because a member turn gets only `toolsets.member`.
Keep `memory` and `skills` out of `toolsets.member`. Add a person that you trust to
`telegram.group_allowed_users`. That person is then an owner.

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
the logs, in tool output, in error text, in the text that goes to the model, and in the cron
run output that `state.db` keeps.

- A token of fewer than 18 characters becomes `***`.
- A longer token keeps its first 6 and its last 4 characters.
- The redactor also masks sensitive query parameters, for example `access_token`, `api_key`,
  `token`, and `password`.
- The redactor masks the password in the user part of an `http` or `https` URL, for example
  `https://user:pass@host`. The password becomes `***`.
- The redactor masks the value of `Authorization: Basic`. The value becomes `***`.
- The redactor masks the value of `--password` and `--token`, in the forms `--password x` and
  `--password=x`. A quoted value counts as one value. The value becomes `***`.
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
- The backup masks each known secret value of 8 or more characters in the messages, the reasoning text of the
  messages, the memory files and the memory notes, the skills, and the scripts. The list of
  known values is the same list that the redactor uses.
- The backup replaces each known secret value in the other strings of `config.yaml` with a
  `${NAME}` reference to its variable. The value works again after a restore when `.env` has
  that name.
- The backup reports other values in `config.yaml` that look like a key. It does not change
  these values.
- The backup also counts the stored values with text that looks like a key, with the patterns
  of the redactor. It does not change these values. It reads at most 64 KB of each value and
  of each file, and it stops at 1000. The report then shows
  `Note: <n> stored values hold text that looks like a key. The archive keeps them. Keep it private.`
  A count at the limit shows as `at least 1000`. The manifest keeps the counts as
  `key_like_cells` and `key_like_files`. The count can include `NAME=value` text and URL query
  parameters.
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
also refuses a text file. An `[image: <path>]` marker in the user text
loads only when its real path is inside the media inbox, `<data>/cache/media/`.
The program resolves `..` and the symbolic links before the check. A marker outside that
folder stays in the text, and the image does not load.

### File tools

The file tools resolve the path before the check:

- They resolve `..` and each symbolic link in the path. A link that points to a protected path
  is refused. A `..` applies to the real folder before it, as in the kernel. So `l/../x` goes to
  the parent of the target of the link `l`, not to the folder of `l`. The coding agent of
  `acp_delegate` uses the same rule for the paths of its permission requests.
- A dangling link counts as its target. A write through the link cannot create the protected
  file.
- They compare the paths after Unicode NFC normalization and case folding. The macOS file
  system ignores case and the normalization form, so another form of a protected path names
  the same file.
- `skill_manage` refuses a write through a symbolic link in `skills/`.

### Shell commands

The gate reads a command as bash does, with a parser for the bash syntax. It removes the quotes
of each word, so `r''m -rf x` is read as `rm -rf x`. It finds each simple command, also in a
pipe, a list, a subshell, a command substitution, and a wrapper. See
[Wrappers](#wrappers). It never runs the command and does not expand a variable. It expands braces as bash does, so
`{rm,-rf,/home}` is read as `rm -rf /home`. It reads the `>|` redirection as `>`.

The rules read three forms of a command:

- The raw text. The hardline rules and the protected path rules read it.
- The rule line of each simple command: the words after quote removal, and the redirects.
- The command view. The view is the raw text with the comments removed. It also leaves out the
  arguments of an `echo` or a `printf` that no pipe reads, and the body of a heredoc of a `cat`
  or a `tee` that no pipe reads. These texts are data, so a rule does not match on them. The
  other rules read the view.

The text of an `echo`, `printf`, `cat`, or `tee` is not data in these cases, because a shell can
run it:

- A word of the command has a process substitution, for example `tee >(sh)`.
- The command is the body of a `coproc`, or the full command has a `coproc`.
- The full command has an `exec` with a redirection, or a redirection that copies to an fd other
  than 1 and 2, for example `>&3`.
- A function or an `alias` in the full command has the name of the program.

A command that the gate cannot read is a different case. See
[Commands that the gate cannot read](#commands-that-the-gate-cannot-read).

A shell write is a `>` or `>>` redirection, or one of these commands with the path as an
argument:

- `tee`, `cp`, `mv`, `install`, `ln`, `rm`, `truncate`, `chmod`, `chown`, `dd`, and `rsync`
- `sed -i`, `gsed -i`, and `busybox sed -i`
- `perl -i`, also `perl -pi` and `perl -i.bak`
- `awk -i inplace` and `gawk -i inplace`
- `ed`, `ex`, `sponge`, and `sqlite3`. `ed` and `ex` also match after the launcher words `sudo`,
  `setsid`, `stdbuf`, `busybox`, `env` (with its options and its `NAME=value` words), `nohup`,
  `command`, `exec`, `nice`, `time`, and `timeout <n>`.

An interpreter with inline code is also a writer: `python`, `python2`, `python3`,
`python3.N`, `perl`, `ruby`, `node`, `deno`, `bun`, `php`, or `lua` with `-c`, `-e`, `-E`,
`--eval`, `-r` (php), `-` (stdin), or a heredoc (`<<`). A flag group that ends in `c` or `e`
also counts, for example `perl -we` or `python3 -Ic`. Options can come before the code flag,
also `-X` and `-W` with their values, for example `python3 -X utf8 -c`. The interpreter can be
anywhere in the command, for example `/usr/bin/python3 -c`, `uv run python3 -c`, or
`setsid python3 -c`. A protected path anywhere after it is a write, also on a later line, for
example in the body of a heredoc. A protected path rule never reads an `always` answer, so an
`always` answer for `python3 -c` does not cover it.

- The rules match the absolute path. For a path in the home folder, they also match the `~`,
  `$HOME`, and `${HOME}` forms.
- The rules match by prefix. A path that starts with a protected path is also dangerous, for
  example `<data>/.envrc` or `<data>/config.yaml.bak`.
- The rules read the text of the command only. They do not resolve a relative path or a
  symbolic link.
- The protected path rules also match a second form of the command. The second form has no
  `'`, no `"`, and no `\` before a character. So `sqlite3 ~/.tether/"state".db` and
  `sqlite3 ~/.tether/state\.db` also match. The other dangerous rules match only the first form.
- In the second form, a word with `*`, `?`, or `[` becomes each protected path that it
  matches, as in bash. So `rm ~/.tether/*` matches the rule of each protected file and folder in
  `~/.tether`. A word matches a protected path when each segment matches. A word for a
  protected folder matches when its first segments match, for example `~/.ss*` or
  `~/.teth*/skills/a/x`. The bash forms `[!x]` and `[[:lower:]]` also work. A glob character at
  the start of a segment does not match a leading `.`. So `rm ~/*` does not match `~/.ssh/`,
  but `rm ~/.*` matches. A word that is not a valid pattern, for example the `[` of
  `[ -f x ]`, does not match. A backtick ends a word, as in bash.

Each command that names `<data>/state.db` is dangerous, also a read. The rule matches the same
path forms as a write rule, and also `state.db-wal`, `state.db-shm`, and `state.db.lock`. Its ID
is `protected_path_use_<slug>`, and its description is `use of the Tether Agent database`. The
rule `sqlite_client_state_db` matches `sqlite3`, `sqlite`, `litecli`, or `sqlite-utils` with the
word `state.db` in the same simple command. It covers `cd ~/.tether && sqlite3 state.db`.
A glob word whose last segment matches `state.db` counts as `state.db`, so
`cd ~/.tether && sqlite3 state*` asks. The segment must have a character that is not a glob
character. So a bare `*`, for example in `sqlite3 app.db "SELECT * FROM t"`, does not count.

A command that names `state.db` asks each time. It has no `session` or `always` answer, and
the smart judge does not judge it. `state.db` holds the `always` answers and the goal gates,
so one answer must not open it for later commands. Use `session_search` to read the
transcripts. The protected path rules come before the other dangerous rules. So a `session`
answer for `recursive_delete` does not permit `rm -rf build; sqlite3 ~/.tether/state.db x`.

These forms still get past the rules. This is a known limit:

- a relative glob after a `cd` that the gate cannot tie to a protected folder, for example
  `cd ~; cd .tet*; sqlite3 sta*`
- brace expansion, for example `stat{e,}.db`, and ANSI-C quotes, for example `$'state'.db`
- a path that a program builds while it runs, for example
  `python3 -c "p=os.path.expanduser('~')+'/.teth'+'er/.env'"`
- `python3 x.py`, and each other interpreter on a file. A script file does not match the
  interpreter rule, so `python3 <data>/skills/x/scripts/a.py` and `node <data>/scripts/job.js`
  do not ask.
- A command that one step writes and a later step runs, for example
  `echo 'rm -rf x' > s.sh && sh s.sh`. The gate does not ask at `echo`, because the text of an
  `echo` is data. It also does not read `s.sh`. This is the same limit as `python3 x.py`.
- An assignment on its own command before the program, for example `PATH=/tmp/p; rm x`. The gate
  finds an assignment only on the same simple command as the program. So an ✅ Always answer for
  `rm x` can resolve `rm` with the old `PATH`.

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
  also `browser_run_js`, can still read a cookie that is not `HttpOnly`. The result of
  `browser_run_js` masks the plain cookie values of the current page: a cookie value of 8 or
  more characters becomes `[cookie value]`. An encoded value, for example a value in base64 or
  in URL encoding, still passes.
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
A cron run and a goal turn are owner turns. A resumed turn in a shared session is a member turn.

A member can talk to the bot. A member can run only `/help`, `/new`, `/reset`, `/stop`,
`/steer`, `/now`, `/todo`, `/sessions`, and `/usage`. Each other command, also a new command,
is owner-only. A member gets `⛔ Only the owner can use /<word>.` `<word>` is the name of the
command, for example `reload-mcp`.

A member turn gets only the toolsets of `toolsets.member`, and never `cronjob` or
`delegate_task`. See [Toolsets](#toolsets).
The prompt of a member session has no owner memory. See
[Memory and background review](#memory-and-background-review).

A shared session has no user part in its key: a forum topic with
`thread_sessions_per_user: false`, an observed chat, or a group with
`group_sessions_per_user: false`. In a shared session, `/new`, `/reset`, `/stop`, and `/now`
are owner-only for a member. The reply is `⛔ Only the owner can use /<word> in a shared session.`
In a shared session, a plain text or a `/steer` of a member
does not steer a running owner turn. The text waits in the queue and runs as its own member
turn. A member text still steers a running member turn. An owner text steers each turn.

In an observed chat, each message of a user starts with a `[name|id]` line.
The text of `/steer` and the arguments of a skill command get the same line.
A line in the text that looks like such a label, or like a group context header, gets a
leading `\`. So a member cannot forge a message of another user.

- `/usage` from a member shows the `session` form. A range form gets
  `⛔ Only the owner can see the usage of all chats. Send /usage session.`
- `/help` from a member lists the member commands and the skill commands. In a shared
  session, it adds the line `In a shared session, only the owner can use /new, /stop, and /now.`
- A busy reply to a member in a shared session has no **⚡ Do this now** button.
- `/sethome` needs an owner. A member gets `⛔ Only the owner can use /sethome.`
- `/approve` and `/deny` need an owner. They answer only the approvals of the same topic.
- `/start` stays open. A skill slash command is a model turn, and a member can use it.
  The turn has the member tools only.

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

A member cannot answer an approval, and a member turn gets only the member tools.
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
- A file that the guard does not scan gives a high finding `unscanned_file`:
  `a file that the guard cannot scan, and that a program can run`. Bash is not the only
  program that runs a file. `node payload` and `python3 payload` also run a file with a NUL
  byte in its first line. A high finding gives `caution`. An image, font, document, archive,
  audio, video, or model file, and a file under a `.git/` folder, give no finding and
  stay in the `not scanned (binary)` line.
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
  `skills.guard` is `false`. The report lists the media files and the files under `.git/` that
  are not text as `not scanned (binary)`. Each other file that the guard cannot scan has an
  `unscanned_file` finding. So a community skill with such a file needs `--force` for
  `tether skills install` and for `tether skills update`.
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

The approval gate gives each MCP tool a class from its name and its hints, and it checks each call
that is not a read. Tether Agent cannot see what an MCP server does with a call. An MCP tool can do
anything that its server can do, so the class is a guess. See
[MCP tool classes](#mcp-tool-classes).

- Enable only the MCP servers that you trust.
- Use the toolsets to hide MCP tools from a channel. Each server has the toolset
  `mcp_<server>`, and the toolset `mcp` has all MCP tools.
- Use `tools.include` and `tools.exclude` to hide single tools of one server from the model.
- A stdio server gets the child environment with no secrets, plus its own `env` block.
- A configured header goes only to a trusted origin: the origin of the server URL and, for an
  SSE server, the origin of the `endpoint` URL that the server sends. An origin is the scheme,
  the host in lower case, and the port. A missing port is 80 for `http` and 443 for `https`.
  A redirect to another origin gets no configured header, and the first such request logs a
  warning that names the origin.
  When a redirect takes the SSE stream to an origin that is not trusted, the program trusts no `endpoint` origin.
  The requests to that origin then get no configured header, and the log names the origin. A
  redirect on the same origin, for example from `/sse` to `/sse/`, keeps the rule.
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
  server again and tries one more time. The retry depends on the class of the tool:
  - A call that did not reach the server is sent again, for each class. Examples are a closed
    connection and a broken pipe on the write.
  - A lost session or a refused connection ends all calls that wait on the connection, also a
    call that the server already ran. Such a call is sent again only for a `read` call.
  - A call that broke after the request reached the server, with an EOF or a reset, is sent
    again only for a `read` call. A timeout never gives a second call.
  - A `write`, `send`, or `human-only` call is not sent again. The model gets
    `The mcp_<server>_<tool> call may have run. Do not send it again. Check the result first, or tell the owner.`

### MCP tool classes

Each MCP tool has one class. The class sets the check of the gate.

| Class | Meaning | Check |
|---|---|---|
| `read` | The tool only reads. | The call runs. The gate writes no log row. |
| `write` | The tool changes data. | The gate asks. ✅ Once, ✅ Session, and ✅ Always are possible. |
| `send` | The tool sends data to other people. | The gate asks each time. Only ✅ Once and ❌ Deny. |
| `human-only` | Only the owner can approve the call. | The owner taps ✅ Once each time. See [Human-only actions](#human-only-actions). |

The classifier reads the tool name. It splits the name at `_`, `-`, `.`, and a camelCase
boundary, and it removes a first word that is `api`, `mcp`, or a word of the server name. It
applies these rules in order. The first rule that matches gives the class.

1. A human-only word set gives `human-only`. See [Human-only actions](#human-only-actions).
2. A read verb as the first word gives `read`, unless the server sets `destructiveHint` to true.
   The read verbs are `get`, `list`, `fetch`, `search`, `read`, `find`, `retrieve`, `lookup`,
   `view`, `check`, `count`, `query`, `describe`, `show`, and `download`.
3. A send word gives `send`. The send words are `send`, `reply`, `respond`, `forward`, `post`,
   `publish`, `tweet`, `retweet`, `broadcast`, `invite`, and `share`.
4. The word `draft` gives `write`.
5. A word `create`, `add`, or `new` with a sent noun (`message`, `comment`, `post`, `email`,
   `mail`, `invoice`, `reply`, `tweet`, or `dm`) gives `send`.
6. A `readOnlyHint` of true, with no `destructiveHint` of true and no write word, gives `read`.
7. Each other name gives `write`.

The hints come from the server. The program keeps them in the tool cache. A cache of an older
version has no hints, so the hints apply after the next `/reload-mcp` or the next restart.

The filter and the overrides are in `mcp.servers.<name>.tools`:

- `tools.include` and `tools.exclude` list raw tool names or patterns with `*`. An empty include
  list keeps all tools. An exclude match wins over an include match. The model does not see a
  hidden tool.
- `/reload-mcp` shows a line for each server with the number of tools that the filter hides. It
  also warns about an entry that matches no tool. `tether doctor` shows the same facts.
- `tools.class` maps a raw tool name to `read`, `write`, or `send`. It replaces the guess of
  the classifier. It cannot change a `human-only` tool. The reload then warns that the entry is
  ignored.
- `approvals.human_only` can raise one tool to `human-only` with the entry
  `mcp:<server>.<tool>`. The list can only raise a class.

The order of the checks for a call that is not `read` is:

1. A `human-only` call asks the owner each time. A cron run, a member turn, and a missing
   Prompter block it. No mode and no grant lowers it.
2. The live mode: the topic mode `full` or `approvals.mode: off` runs the call.
3. A member turn asks each time, with only ✅ Once and ❌ Deny.
4. A `write` call runs when a ✅ Session answer or an ✅ Always row covers the tool and the server
   entry.
5. A cron run runs the call only with `approvals.cron_mode: approve`.
6. A `send` call asks each time.
7. The owner decides.

There is no smart judge for MCP calls, because the arguments are content that can argue with a
judge. In `read-only` mode, only a `read` call runs. A call that is not `read` fails when no gate
is present.

The grant key of a `write` call is `mcp:<server>.<tool>` and the config hash of the server
entry. The hash has 16 hex characters. It covers the command, the URL, the transport, and the
`env` and `headers` entries. Each `env` and `headers` value enters the hash as its own hash, so
the hash holds no secret value. A change of the server entry ends the grants for its tools. A
change of the `tools` block or of a timeout does not.

The prompt shows one `key: value` line for each argument, in key order. Each value is one line,
masked by the redactor, and cut at 300 characters. The lines stop at 3000 characters, and a last
line names the arguments that the prompt does not show. A `send` call uses the header
**📤 Send on your behalf?** A human-only call uses the header **🔒 Human-only action**.

## Host execution

Tether Agent has no container backend in v1. Commands run on the host as the Tether Agent user,
with the permissions of that user.

- Run Tether Agent as a user with only the permissions that the agent needs.
- Do not run Tether Agent as root. `tether setup` refuses a unit that runs the gateway as
  root: a system unit with no `User=` line, or a user unit when setup runs as root. The error
  is:
  `setup: the unit would run the gateway as root. Run setup as your user (with sudo for --system), or add --allow-root to run the gateway as root.`
- The gateway logs a warning at start when it runs as root:
  `gateway runs as root: each command of the agent runs as root too. See "Host execution" in docs/SECURITY.md.`
- `tether doctor` warns when the installed unit runs the gateway as root: a system unit with
  no `User=` line or with `User=root`, or a user unit under `/root`. The next step is
  `run tether setup --system with sudo from your user account`.
- Keep the approval mode at `manual` or `smart` on a machine with important data.
- At start, Tether Agent sets mode 0700 on a data folder with group or other permissions. It
  sets mode 0600 on `state.db`, `state.db-wal`, and `state.db-shm` in the same case. Each
  change logs a warning with the old mode. `tether doctor` warns about such a data folder.

The systemd unit of `tether setup` has no containment settings.
Each containment setting breaks a supported use:

- `NoNewPrivileges` and `CapabilityBoundingSet=` stop the sudo path of `SUDO_PASSWORD`.
- `ProtectSystem=strict` stops `tether update` from the gateway, and host administration.
- `ProtectHome=read-only` stops an edit of a repository in the home folder.
- `RestrictNamespaces` and `SystemCallFilter` stop the Chrome sandbox of the browser tools.
- `RestrictAddressFamilies` without `AF_NETLINK` stops `ip` and `ss` in the terminal.
- `PrivateTmp` and the `Protect*` settings in a user unit need user namespaces. They also
  stop sudo.

You can add a drop-in file:
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
2. A script that has a string literal with a human-only program, for example
   `subprocess.run("useradd bob")`, asks each time. It has only ✅ Once and ❌ Deny. See
   [Human-only actions](#human-only-actions).
3. With `approvals.mode: off`, or with the topic mode `full`, the script runs.
4. The dangerous rules run on the text. A literal match, for example `rm -rf` in a
   `subprocess` string or a protected path, makes the script ask each time.
5. A script with no literal match runs when the session has a ✅ Session answer for the same
   script bytes. The key is the SHA-256 hash of the script. The saved `always` answers do not
   apply. A member turn reads no `session` answer, so its script asks each time.
6. In smart mode, the judge gets a script prompt and at most the first 8000 characters
   (`approvals.smart_script_max_chars`). A low-risk answer runs this script only. A longer
   script goes to the user. A script with a literal match of a protected path rule does not go
   to the judge.
7. Else the gate asks the user.

The prompt uses the rule `execute_code` with the description `run a Python script on the host`.
A literal match adds its description, for example
`run a Python script on the host (recursive delete)`. The answers are `once`, `session`, and
`deny`. A `session` answer of an owner covers only the same script bytes. A changed script asks
again. In manual mode, each new script asks.
Use smart mode to let the judge review a new script first.

In a cron run, each script follows `approvals.cron_mode`. See [Cron runs](#cron-runs).

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
