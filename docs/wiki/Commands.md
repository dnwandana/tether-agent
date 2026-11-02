# Commands

Tether Agent has two kinds of commands:

- **CLI subcommands** run in your shell, for example `tether setup`.
- **Slash commands** go into a chat, for example `/new`. They work in Telegram and in
  `tether chat`.

## CLI subcommands

### Help

Each of these forms shows the help of one command:

```sh
tether help update
tether update help
tether update --help
```

`tether` with no command shows the main help. An unknown command shows the nearest match.

### The data dir

Most commands take `--data-dir DIR`. Tether Agent selects the data dir in this order:

1. `--data-dir`
2. the `TETHER_HOME` environment variable
3. `~/.tether`

Use the same data dir for all commands. A different `TETHER_HOME` in another terminal changes a
different `state.db`.

You can put flags after the positional arguments.

### The table

| Command | Purpose | Flags | Example |
|---|---|---|---|
| `setup` | Runs the setup wizard in five steps: the primary provider (a preset or Custom), the fallback chain, Telegram, the host programs, and the systemd unit. See [Providers](Providers). | `--system` writes a system unit instead of a user unit. Under `sudo`, the data dir and its files belong to the user who called `sudo`. | `tether setup` |
| `doctor` | Checks the config, `.env`, each provider entry, Telegram, `state.db`, the last answer, the host programs, the systemd unit, MCP, the disk, and the release. Exits with 1 if a check fails. | | `tether doctor` |
| `chat` | Starts a chat in the terminal. | `--log-stderr`, `--log-level` | `tether chat` |
| `gateway` | Runs Telegram and cron until it gets SIGINT or SIGTERM. | `--log-stderr`, `--log-level` | `tether gateway` |
| `sessions list` | Shows the 50 newest sessions. | | `tether sessions list` |
| `sessions export <id>` | Writes one session as redacted JSONL. The default file is `<data dir>/exports/<id>.jsonl`. | `--out FILE` | `tether sessions export <id> --out chat.jsonl` |
| `skills` | The subcommands are `list`, `install`, `uninstall`, `check`, `update`, and `reset`. See [Skills](Skills). | `--available`, `--skill`, `--ref`, `--yes`, `--force` | `tether skills install openai/skills --skill playwright` |
| `pairing` | The subcommands are `list`, `approve`, and `revoke`. See [Telegram](Telegram#pairing). | | `tether pairing approve K7Q2MXRP` |
| `usage [today\|7d\|30d]` | Shows the calls, the tokens, the speed, and the cost of each model. The default range is `today`. The cost comes from the price that Tether Agent stored with each call. | | `tether usage 7d` |
| `backup` | Writes a backup archive of the data dir. | `--out PATH` or `--out -` | `tether backup` |
| `restore FILE` | Restores a backup archive. `-` reads from stdin. | `--yes` | `tether restore --yes backup.tar.gz` |
| `update` | Installs the latest release over the binary. | `--check`, `--tag vX.Y.Z`, `--force`, `--restart`, `--no-restart` | `tether update --check` |
| `version` | Shows the version. | | `tether version` |
| `help [command]` | Shows the main help or the help of one command. | | `tether help skills` |

`chat` can run at the same time as the gateway. Only one gateway can run for each data dir.

For backup, restore, and update, see [Running as a Service](Running-as-a-Service).

## Slash commands

Send a slash command as a chat message. The "Busy" column shows if the command works while a
task runs.

| Command | Arguments | Effect | Telegram buttons | Busy |
|---|---|---|---|---|
| `/new`, `/reset` | | Stops the task, clears the queue, and starts a new session. The new session has no `/model` pin, no `/effort` level, and no `/prompt` text. Only `/fork` copies them. The reply shows the model and the context size. An active goal of the old session pauses. | | yes |
| `/sessions` | `[page] [search]` or `archived` | Lists the sessions of this chat, 8 for each page. | A number button for each session. ◀ Prev and Next ▶. | yes |
| `/resume` | `[<id>]` or `[page] [search]` | Changes to another session. With no ID, it shows the list. It also takes a session out of the archive. An active goal of the current session pauses. An active goal of the session that you change to continues. | Number buttons | no |
| `/rename` | `<title>` | Changes the title of the session. With no argument, it shows the title. | | no |
| `/archive` | | Hides the session from `/sessions`. An active goal of the current session pauses. | ↩️ Undo, 🆕 New session | no |
| `/delete` | `[<id>] [confirm]` | Deletes a session after a confirmation. With no ID, it shows the list. It also deletes the goal of the session. | 🗑 Delete, Cancel | no |
| `/model` | `[<provider>[:<model>]]`, `<provider> config`, `providers`, or `clear` | With no argument or with `providers`, it shows the provider entries and the model that answers now. `/model <provider>` shows the model list of the entry. A model from the list, `config`, or `<provider>:<model>` sets the model for this session. `clear` uses the primary provider again. | One button for each provider entry. The list has 8 model buttons for each page, ◀ Prev and Next ▶, ↩ Providers, and ↻ Refresh. After a change, ↺ Use the default. | no |
| `/effort` | `[<level>]` or `default` | Sets the reasoning effort for this session. See [/model and /effort](#model-and-effort). | `default` and the seven levels. After a change, ↺ Use the default. | no |
| `/prompt` | `set <text>`, `show`, or `clear` | Replaces the base prompt for this session only. | 🧹 Use the base prompt | no |
| `/fork` | `[title]` | Copies the session and changes to the copy. An active goal of the current session pauses. | `↩️ Back to "<title>"` | no |
| `/compact` | | Summarizes the older messages now. | | no |
| `/reload-mcp` | | Reloads the MCP servers. The next request does not use the prompt cache. | | no |
| `/usage` | `[today\|7d\|30d\|session]` | Shows the tokens and the cost for each model. The default range is `today`. `session` also shows the number of compactions. `session` also counts the goal judge calls of the session. | Today, 7 days, 30 days, Session | yes |
| `/help` | `[skills]` | Shows the main commands and the first 10 skill commands. `/help skills` shows all skill commands. | | yes |
| `/stop` | `[keep]` | Stops the task. `keep` keeps the waiting messages. It also pauses an active goal. | | yes |
| `/steer` | `<text>` | Adds guidance to the running task. | ⚡ Do this now | yes |
| `/now` | | Stops the task and runs your waiting message now. | | yes |
| `/goal` | `[<text>]`, `status`, `show`, `draft <text>`, `pause`, `resume`, `clear`, `wait <pid> [reason]`, `unwait`, or `gate list\|add <command>\|remove <n>\|clear` | Sets a goal and starts the first goal turn. After each turn, a judge model checks the answer, and the next goal turn starts until the goal is done. With no argument, it shows the status. See [Goals](Goals). | Pause and Clear for an active goal. Resume and Clear for a paused goal. On a goal pause notice: Resume and Clear. For a dangerous gate: Add gate and Cancel. | yes, except a new goal text and `draft` |
| `/subgoal` | `[<text>]`, `remove <n>`, or `clear` | Adds a criterion to the goal. With no argument, it lists the criteria. See [Goals](Goals). | | yes |
| `/todo` | | Shows the task list of the session. It makes no model call. See [Goals](Goals#the-task-list). | | yes |
| `/restart` | | Restarts the gateway. It waits for the running turns first. | | yes |

In the Telegram command menu, `/reload-mcp` is `/reload_mcp`, because Telegram does not permit
`-` in a command name. The two names do the same thing. The menu does not show `/start`,
`/reset`, `/resume`, and `/subgoal`, but they work.

### Skill commands

Each skill also has a slash command. `/<skill> [request]` runs the skill with the request, for
example `/arxiv transformers 2026`. A built-in command wins over a skill with the same name. In
the CLI, an unknown name gets `Unknown command /<name>. Send /help to see the commands.` In
Telegram, a slash text that is not a command and not a skill goes to the agent as a normal
message. See [Skills](Skills#run-a-skill-with-a-slash-command).

### /model and /effort

`/model` and `/effort` change only the current session. The change applies from the next
message. `/model` does not change `config.yaml`.

`/model` takes these forms:

| Form | Result |
|---|---|
| `/model` | Shows the provider entries and the model that answers now. |
| `/model <provider>` | Shows the model list of the entry, page 1. |
| `/model <provider> <n>` | Shows page `n` of the list. A page past the end shows the last page. |
| `/model <provider> refresh` | Gets a new list from the provider, then shows page 1. |
| `/model <provider> config` | Uses the model that `config.yaml` sets for the entry. |
| `/model <provider>:<model>` | Uses that model. Each model ID works, also an ID that is not in the list. |
| `/model clear` | Uses the default again. |
| `/model providers` | Shows the same reply as `/model`. The ↩ Providers button sends this form. After a tap, the reply replaces the message of the button. |
| `/model <provider> use <i> <stamp>` | The form that a model button sends. It uses the model at position `i` of the list. The `stamp` identifies the list, so an old button does not select a wrong model. |

> **Caution:** send `/model <provider> config` to use the model of `config.yaml`.
> `/model <provider>` now shows the model list, and it does not change the model.

- The reply names an entry with its provider when the two names are not the same, for
  example `primary (OpenRouter)`.
- The list comes from the provider. It does not show models that cannot chat. Refer to
  [Providers](Providers).
- For OpenRouter, the list has two groups: the 10 models that are most used this week, then
  the 5 best models for agents. It shows only models with tool support.
- The ✓ marks the model that this session uses with the entry: the model of the session, else
  the model in `config.yaml`.
- `/model` keeps a list for 1 hour. `/model <provider> refresh` and ↻ Refresh get a new list.
- A list call can take up to 10 seconds. The chat waits for the call.
- When the list call fails, the reply shows the cause. Send `/model <provider>:<model>`. When
  the entry has a model in `config.yaml`, tap `Use <model>` to use that model.
- An unknown provider entry gets a reply with the list of provider entries.
- An entry is not usable when it has no API key or no `model`. Its button has the ⚠️ mark.
  On the CLI, its label has `not usable` in the brackets, for example
  `zai (Z.AI, not usable)`. Each `/model <provider>` form for the entry shows the cause, for
  example `⚠️ **zai is not usable**`, and changes nothing.
- When the stored model of the session names an entry that is not usable, `/model` shows a
  ⚠️ line, for example `⚠️ zai has no API key, so calls go to primary.` The calls go to the
  default chain. Tap ↺ Use the default, or send `/model clear`. After you fix the config and
  restart, the stored model works again.
- Another form gets the reply `⚠️ **Unknown form: <args>**` with the line
  `Send /model, /model <provider>, /model <provider>:<model>, or /model clear.`
- The words `clear` and `providers` are always commands. Thus an entry with one of these names
  cannot open its list with `/model <entry>`. Send `/model <entry> 1` for page 1 of its list.
- A `stamp` has 4 characters from `0-9` and `a-f`. When the list changed after the bot sent the
  button, the model does not change. The reply is page 1 of the new list with the line
  `The list changed. Select the model again.`

The first entry of the fallback chain gets a wait time of 60 seconds after it fails. In that
time, the replies of `/model` and `/new` change:

- `/model` names the entry that answers now and its model.
- `/new` shows `Model: <model> (fallback)`, with the model of the entry that answers now.
- Both replies add a ⚠️ line with the cause and the entry that answers now. An example is
  `⚠️ The primary model <model> failed less than 1 min ago (HTTP 503). Calls go to <entry> until the primary answers again.`

After the wait time, the replies show the first entry again. See
[How It Works](How-It-Works#fallback-chain).

`/effort <level>` takes one of these levels: `none`, `minimal`, `low`, `medium`, `high`,
`xhigh`, or `max`. The command does these steps:

1. With no argument, it shows the reasoning effort, the provider entry, the model, and the
   source of the value.
2. `default` removes the value of the session. Then the `effort` key of the provider entry
   applies, else the provider default.
3. When the model rejected the level, the reply shows a ⚠️ line, and the provider default
   applies.

The source is "this session", "from config", or "provider default". For the `effort` key, see
[Providers](Providers#reasoning-effort). For the fallback chain, see
[Providers](Providers#the-fallback-chain).

### /restart

`/restart` exists only in the gateway. The gateway waits up to `agent.restart_drain_timeout_s`
for the running turns, and then restarts. With the default config, a turn that the restart
cuts continues after the restart. The chat that sent `/restart` gets "♻ The gateway
restarted. Your session continues." See [Running as a Service](Running-as-a-Service).

### Commands for one channel or one role

| Command | Where | Effect |
|---|---|---|
| `/start` | Telegram only | Shows a welcome message with 📋 Sessions, 🧠 Model, and 📊 Usage buttons. |
| `/sethome` | Telegram only | Makes this chat or topic the home channel for cron results. |
| `/approve` | Telegram only | `[session\|always] [all]`. Approves the oldest waiting command, or all of them with `all`. |
| `/deny` | Telegram only | `[all]`. Denies the oldest waiting command, or all of them. |
| `/restart` | Gateway only | `tether chat` does not have it. |
| `/exit`, `/quit` | CLI only | Ends the chat. |

`/start`, `/sethome`, `/approve`, and `/deny` do not go through the gateway queue. Thus they
work also while a task waits for an approval.

### Buttons in the CLI

The CLI shows no buttons. Each reply gives the command to type instead, for example `Send
/resume <id> to undo.` The CLI asks for a confirmation with `[y/N]`.

In the CLI, `/model <provider>` prints the full list with full model IDs, on one page. The last
line gives the command to type:

```text
🧠 primary (OpenRouter) · 15 models
Most used this week (1 to 10), then best for agents (11 to 15). Only models with tool support.
   1) deepseek/deepseek-v4.1-flash-1017
   …
   9) z-ai/glm-5.3  ✓
   …
  15) meta/muse-spark-1.3
Send /model primary:<model> to use a model.
```

## While the agent is busy

The gateway runs one turn at a time for each session. This section tells you what happens when
you send a message while a turn runs. It applies to Telegram.

### Text becomes a steer

A plain text message goes into the running task as guidance.

1. The gateway joins the texts that arrive within 350 ms. It waits a maximum of 1 second.
2. The agent gets the text after the current tool step, as "User guidance: …".
3. The bot replies "↪️ Got it. I'll do this after the current step." with a **⚡ Do this now**
   button.
4. When the agent reads the text, the bot edits the reply to "✅ The agent read your note." and
   removes the button.

If the turn ends before the agent reads the steer, the text runs as the next turn. Then the bot
edits the reply to "↪️ The task ended first. Your note runs as a new message." This also applies
to a steer that arrives in the last tool round of a turn.

### Media waits in the queue

A photo, a file, or a voice note waits in the queue. The bot replies "⏳ Queued. I'll answer after
the current task." with a **⚡ Do this now** button. The queued messages run after the task, one
at a time. When the queued message starts, the bot edits its reply to "▶️ The agent works on your
message now."

The bot sends a maximum of one steer reply and one queue reply for each session every 30 seconds.
A new turn can get a busy reply at once. After the bot edits a busy reply, the next message gets a
new reply at once.

### Commands during a task

- A command with "yes" in the Busy column runs at once.
- Another command gets the reply "⏳ **Busy** `/x` cannot run during a task. Wait for the
  answer, or stop the task." with a **⏹ Stop the task** button.
- A slash text that is not a command, for example a skill command, waits in the queue like a
  normal message. The bot replies "⏳ Queued. I'll answer after the current task."

During a goal, the session stays busy from one goal turn to the next. A command that does
not work while a task runs gets the line "A goal runs. Send /goal pause first."

### Stop or take over

| Command | Effect |
|---|---|
| `/stop` | Stops the task. Clears the queue and the waiting steer. Stops the background subagents. The reply is "⏹ Stopped. The session is safe. Send a message to continue." The steer reply and the queue replies change to "⏹ Not sent. The task stopped." An active goal pauses, also when no task runs. The reply adds "⏸ Goal paused. Use /goal resume to continue." With no task, the reply is only this line. |
| `/stop keep` | Stops the task. Keeps the queue and the waiting steer. The waiting messages run one at a time. The steer reply changes to "↪️ The task ended first. Your note runs as a new message." An active goal pauses, also when no task runs. The reply adds "⏸ Goal paused. Use /goal resume to continue." With no task, the reply is only this line. |
| `/now` | Stops the task. The waiting steer runs first, and then the queue. The reply is "⚡ Taking over. The current answer stopped." The steer reply changes to "↪️ The task ended first. Your note runs as a new message." An active goal stays active. |
| `/steer <text>` | Adds the text to the running task, like a plain text. With no task, the text runs as a normal message. |

`/now` does nothing when no message of yours waits. Then the task continues, and the reply is
"✅ Nothing waits. The agent already has your message." A message that the gateway made, for
example a background result, does not count as waiting. A skill command in the queue counts as
waiting.

The **⚡ Do this now** button sends `/now` for the turn of the reply. The bot edits the message of
the button with the result. A button from an older turn stops nothing. After such a tap, the bot
still edits the message when the queued message starts. The button goes away when the steer or
the queued message is over, and when the gateway restarts.

When the agent did not start yet, `/steer <text>` replies "⏩ Note saved". The text then
waits in the queue.

### In the CLI

`tether chat` runs one turn at a time and reads input only between turns. It has no queue and
no steer.

- Ctrl-C during a turn stops the turn. The CLI prints "⚡ Stopped. You can continue this
  session."
- Ctrl-C at the prompt, Ctrl-D, `/exit`, or `/quit` ends the chat.
- Ctrl-C during a goal turn also pauses the goal. The CLI prints "⏸ Goal paused — turn was
  interrupted. Use /goal resume to continue, or /goal clear to stop."
- A goal runs its turns one after the other, with no input between them.
- The end of the chat, for example with `/exit`, pauses an active goal. The CLI prints "⏸ Goal
  paused. Use /goal resume to continue."
- All `tether chat` processes of one OS user share one active session. The start and the end
  of a second `tether chat` also pause the goal of the first chat. Run one `tether chat` at a
  time for a goal, or send `/goal resume` in the first chat.
- `/stop` and `/stop keep` reply "💤 No task to stop". When a goal waits at the prompt, they
  pause the goal and reply "⏸ Goal paused. Use /goal resume to continue." instead.
- `/now` replies "✅ Nothing waits. The agent already has your message."
- `/steer <text>` runs the text as a normal message.

## See also

- [Telegram](Telegram): the bot setup and the chat display.
- [Cron](Cron): scheduled jobs.
- [Running as a Service](Running-as-a-Service): `/restart`, update, and backup.
