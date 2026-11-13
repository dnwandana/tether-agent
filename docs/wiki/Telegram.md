# Telegram

Tether Agent uses a Telegram bot as its main chat channel. The `tether gateway` command runs the
bot. This page tells you how to set up the bot, who can talk to it, and what you see in the chat.

For all `telegram.*` keys, see
[docs/CONFIG.md](https://github.com/dnwandana/tether-agent/blob/main/docs/CONFIG.md#telegram).

## Set up the bot

### With the setup wizard

Stop the gateway before you start. Telegram permits only one program to read the updates of a
bot. If the gateway runs, the wizard stops with "Stop the tether gateway, then run tether setup
again".

1. Open [@BotFather](https://t.me/BotFather) in Telegram.
2. Send `/newbot` and follow the questions.
3. Copy the bot token.
4. Run `tether setup`.
5. At "Set up Telegram? [y/N]", type `y`.
6. Paste the token at "Bot token from @BotFather". The wizard checks the token and shows the bot
   name.
7. Send `/start` to your bot in a private chat. The wizard waits for 5 minutes.
8. At `Approve <name> (<id>) as the owner? [y/N]`, type `y`.

The wizard then does these steps:

- It writes the token to `.env` as `TELEGRAM_BOT_TOKEN`.
- It writes `telegram.bot_token: ${TELEGRAM_BOT_TOKEN}` to `config.yaml`.
- It adds your user id to `telegram.allowed_users`.
- It sets `telegram.enabled: true`.

If you type `n` at the owner question, the wizard prints a `tether pairing approve telegram
<code>` command. Run it later to approve yourself.

### With a manual config

You can also edit the files yourself.

1. Add the token to `<data dir>/.env`:

   ```sh
   TELEGRAM_BOT_TOKEN=123456:ABC...
   ```

2. Add the Telegram section to `<data dir>/config.yaml`:

   ```yaml
   telegram:
     enabled: true
     bot_token: ${TELEGRAM_BOT_TOKEN}
     allowed_users: [123456789]   # your Telegram user id
   ```

When `telegram.bot_token` is empty, Tether Agent uses the `TELEGRAM_BOT_TOKEN` variable.

### Start the gateway

1. Run `tether gateway`.
2. Send a message to the bot.

The gateway checks the token when it starts, and it stops at once if the token is bad. To keep
the bot on all the time, run the gateway as a systemd service. See
[Running as a Service](Running-as-a-Service).

The gateway reads the updates with the `getUpdates` long poll. It has these rules:

- It saves the update offset in `state.db` before it handles an update. Thus a restart does
  not handle an update a second time.
- The messages of one chat go to the gateway one at a time, in their order.
- The messages of different chats run in parallel.
- A button tap does not wait for the messages of its chat.
- An edited message counts as a new message.
- After a failed call, the gateway waits 1 second, then 2, then 4, up to 30 seconds.

## Pairing

Pairing lets a new user ask for access without a config change. The bot replies to an unknown
user with a pairing code. The owner approves the code on the server.

The bot sends this reply:

```text
🔐 Pairing needed …
tether pairing approve telegram <code>
```

The pairing codes have these rules:

- A code has 8 characters from `A-Z` and `2-9`. The characters `0`, `O`, `1`, and `I` are not
  used, because they look the same.
- A code expires after 1 hour (`telegram.pairing_code_ttl_s`).
- A user can get one new code every 10 minutes.
- A platform can have a maximum of 3 codes that wait for approval.
- After 5 failed approvals, the platform locks for 1 hour.
- The database keeps only a salted SHA-256 hash of each code.

Use these commands on the server:

| Command | Effect |
|---|---|
| `tether pairing list [platform]` | Shows the waiting codes and the approved users. |
| `tether pairing approve [platform] <code>` | Approves the user of the code. |
| `tether pairing revoke [platform] <user id>` | Removes the access of a user. |

The default platform for `approve` and `revoke` is `telegram`. `tether pairing list` with no
platform lists all platforms. A running gateway sees a change at once. You do not have to
restart it.

## Who can talk to the bot

The bot checks a sender in this order:

1. `telegram.group_allowed_chats`: each user in these groups, as a member. `*` permits all groups.
2. `telegram.allow_all_users`: every user. Use it only for a test bot.
3. Users that you approved with pairing.
4. `telegram.allowed_users`, and in groups also `telegram.group_allowed_users`. `*` permits
   all users.

With no allow list and no `allow_all_users`, the bot permits nobody. The bot ignores an unknown
sender in a group. `telegram.group_allowed_users` does not permit direct messages.

`telegram.unauthorized_dm_behavior` controls the reply to a direct message from an unknown user:

- `pair`: the bot sends a pairing code.
- `ignore`: the bot does not reply.
- `""` (the default): `ignore` when an allow list is set, else `pair`.

> **Note:** The setup wizard adds your id to `telegram.allowed_users`. After that, the default
> is `ignore`, so the bot does not reply to new users. To let new users ask for a pairing code,
> set `telegram.unauthorized_dm_behavior: pair`.

### Owners and members

The bot has owners and members. An owner is a user that one of these admits:

- pairing
- `telegram.allowed_users`
- `telegram.group_allowed_users`, in a group
- `telegram.allow_all_users: true`

A `*` entry counts. A user that only `telegram.group_allowed_chats` admits is a member. A
message with no user ID is from a member. The CLI is always the owner.

A member can talk to the bot and run `/help`, `/new`, `/reset`, `/stop`, `/steer`, `/now`,
`/todo`, `/sessions`, and `/usage`. Another command gets `⛔ Only the owner can use /<word>.`
In a shared session, `/new`, `/reset`, `/stop`, and `/now` get
`⛔ Only the owner can use /<word> in a shared session.` A shared session is a forum topic with
`thread_sessions_per_user: false`, an observed chat, or a group with
`group_sessions_per_user: false`.

In a shared session, a busy reply to a member has no **⚡ Do this now** button, because `/now`
is owner-only there. `/help` for a member in a shared session adds the line
`In a shared session, only the owner can use /new, /stop, and /now.`

Only an owner can answer an approval and send `/sethome`, `/approve`, or `/deny`.
Add each trusted group member to `telegram.group_allowed_users`.

## Groups and topics

### Privacy mode

By default, BotFather turns on privacy mode for a bot. In privacy mode, the bot receives only
commands, mentions, and replies in a group. If you want the bot to see every group message, do
these steps:

1. Send `/setprivacy` to @BotFather.
2. Select your bot.
3. Select `Disable`.
4. Remove the bot from each group.
5. Add the bot to each group again.

`tether doctor` shows a warning when privacy mode blocks the group messages that your config
needs.

### When the bot answers in a group

These keys control the answers in groups. The bot applies them in this order:

| Key | Effect |
|---|---|
| `allowed_topics` | The bot answers only in these forum topics. |
| `ignored_threads` | The bot ignores these threads. |
| `exclusive_bot_mentions` | The bot ignores a message that mentions only another bot. |
| `allowed_chats`, `guest_mode` | The bot answers only in these groups. Guest mode also permits a direct mention in other groups. |
| `free_response_chats` | The bot answers every message in these groups, with no mention. |
| `require_mention` | The bot answers only to a mention, a reply to the bot, or a `mention_patterns` match. |
| `observe_unmentioned_group_messages` | The bot saves the messages that it does not answer as context. |

All these keys are in the `telegram` section.

A mention or a command in a rich message counts like a mention in a plain text. A mention in
code or in a link does not count.

### Sessions in groups and topics

Each chat has its own session. The session key has this form:

- Direct message: `telegram:dm:<chat>[:<thread>]`
- Group: `telegram:group:<chat>[:<thread>][:<user>]`

Two keys add the user to the key:

- `telegram.group_sessions_per_user` (default `true`): each user in a group without topics has a
  separate session.
- `telegram.thread_sessions_per_user` (default `false`): each user in a forum topic has a
  separate session.

## The home channel

Cron jobs that you create from the CLI send their results to the home channel. To set it, send
`/sethome` in the chat or topic that you want. The command saves the chat in `state.db`.
Only an owner can send `/sethome`. A member gets `⛔ Only the owner can use /sethome.`

You can also set a first value in the config:

- `telegram.home_chat`: the home chat.
- `telegram.home_thread`: the home topic.
- `telegram.cron_thread`: the topic for cron results. It overrides `home_thread` for cron only.

When the gateway starts with a new version, the home chat gets a message such as "♻ Tether
updated from X to Y and restarted." This occurs after `/restart` and after `tether update`
with a restart. The chat that sent `/restart` gets "♻ The gateway restarted. Your session
continues." If you sent `/restart` from the home chat, that chat gets only this second
message.

## The command menu

The gateway sets the bot command menu when it starts. The menu shows these commands, in this
order:

`/new`, `/sessions`, `/stop`, `/steer`, `/now`, `/goal`, `/todo`, `/model`, `/effort`, `/usage`,
`/help`, `/rename`, `/fork`, `/compact`, `/prompt`, `/archive`, `/delete`, `/approve`, `/deny`,
`/sethome`, `/reload_mcp`, and `/restart`.

Telegram does not permit `-` in a command name, so the menu shows `/reload-mcp` as
`/reload_mcp`. Both names work. `/start`, `/reset`, `/resume`, and `/subgoal` are not in the
menu, but they work. See [Commands](Commands) for each command.

After the built-in commands, the menu shows the skill commands in name order, with `_` in place
of `-`. See [Skills](Skills#run-a-skill-with-a-slash-command). These rules apply:

- The menu has a maximum of 60 entries (`telegram.menu_max_commands`, with the 22 built-in commands). Send `/help skills` to see all skill commands.
- A skill command with a name of more than 32 characters is not in the menu.
- The gateway sets the menu only when it starts. A new skill shows in the menu after the next
  start.
- A skill command that is not in the menu still works when you type it.
- When Telegram refuses the menu with the skills, the gateway sets the built-in commands only.

`/start` shows a welcome message with 📋 Sessions, 🧠 Model, and 📊 Usage buttons.

## What you see in the chat

### Typing and tool status lines

While the agent works, the chat shows "typing". This includes the time for media downloads and
transcription.

The bot also sends one status message and edits it as the tools run. Each line shows the tool and
a short preview of its main argument, for example the command or the search query:

```text
💻 terminal: "ls -la"
🔍 web_search: "go 1.26 release notes" (×2)
🔀 [1/3] web_fetch "https://example.com"
🤖 acp_delegate: "Fix the flaky cron test"
🔀 [claude-code] edit "internal/cron/scheduler.go"
```

The status lines have these rules:

- A preview has a maximum of 40 characters (`agent.tool_preview_chars`).
- In the default mode, each tool has one line. A new call of the same tool replaces the
  preview with its own preview and adds the count `(×N)`.
- A line that starts with `🔀 [i/n]` comes from a subagent. Each subagent has its own lines.
- A line that starts with `🔀 [<agent>]`, for example `🔀 [claude-code]`, comes from a coding
  agent of `acp_delegate`. The line shows the kind and the title of one tool call of the coding
  agent. See [Coding agents](Tools#coding-agents).
- The bot edits the status message a maximum of one time every 1.5 seconds (`telegram.status_interval_s`).
- After a streamed answer, the next tool line starts a new status message below the answer.
- A status message that gets too long continues in a new message.

`telegram.tool_progress` sets the detail:

| Value | Effect |
|---|---|
| `off` | No status message. |
| `new` | A line when the tool changes. |
| `all` | One line per tool, with a count. This is the default. |
| `verbose` | Each call, with the argument names and the arguments as JSON on the next line. |

Set `telegram.cleanup_progress: true` to delete the status message after a successful run.

### Goal and plan messages

A goal turn and a task list send their own messages. These messages are silent, so they do not
ring the phone. They are never in a status message. See [Goals](Goals).

- At the start of each goal turn, the goal line goes out as its own message: "↻ Goal n/max:
  reason", "↻ Goal n/max: retry after a failed turn.", or "▶ Goal barrier lifted — resuming."
- When the agent uses `todo_list`, the plan goes out as plan messages. A plan message closes the
  open status message. The next tool line starts a new status message below the plan message.
- The status message shows no `todo_list` line. A line from a subagent, `🔀 [i/n] todo_list`,
  stays.

The bot sends three types of plan messages:

1. The start list goes out when a new plan starts. It shows all items in list order. A subtask
   has two spaces for each parent above it, as in `/todo`:

   ```text
   📋 Plan 0/3
   [>] Find the flaky test
   [ ] Replace the real clock with a fake clock
   [ ] Run the cron tests 20 times
   ```

2. The change message goes out for a `todo_list` call that completes, cancels, or adds items.
   It has one line for each change:

   ```text
   ✅ 1/3 Find the flaky test
   🚫 Run the cron tests 20 times
   ➕ Run the cron tests 50 times
   ```

   - `✅ n/m` shows a completed item. `n` is the number of completed items. `m` is the number
     of items that are not cancelled.
   - `🚫` shows a cancelled item.
   - `➕` shows a new item.

   Other changes send no message, for example an item that starts, or a text edit.
3. The done list goes out when a `todo_list` call closes the last open item. It replaces the
   change message of that call. A cancelled item does not count:

   ```text
   📋 Plan done 3/3
   [x] Find the flaky test
   [x] Replace the real clock with a fake clock
   [~] Run the cron tests 20 times
   [x] Run the cron tests 50 times
   ```

These rules apply to the goal message and the plan messages:

- A plan continues over many turns. A goal turn or a new message does not send the start list
  again.
- A turn that ends with open items sends no plan message. Send `/todo` to see the list.
- An answer message waits for the plan messages before it, for a maximum of 30 seconds. Thus
  the chat shows the messages in the order of the events. A `/stop` ends the wait.
- With `telegram.tool_progress: off`, no plan message goes out. The goal line still goes out
  as one silent message.
- With `telegram.cleanup_progress: true`, a successful turn deletes the goal message, the
  start list, and the change messages with the status message. The "📋 Plan done" message
  stays. In `off` mode, the goal message also stays.
- A failed turn deletes nothing.
- After a gateway restart, the goal turns show no plan messages until you send a message.
  Their goal line is a message that is not silent.

### Streamed answers

The answer appears while the model writes it:

- In a direct message, the answer streams into a Telegram draft.
- In a group, the bot edits one message, with a ` ▉` cursor at the end.

`telegram.streaming.transport` sets the method: `auto`, `edit`, or `draft`. `auto` and `draft`
use drafts in direct messages and edits in groups.

All sends and edits of the bot in one chat share one budget:

- In a group, the bot sends or edits at most 18 messages in each minute.
- In a private chat, the bot sends or edits at most one message each second.
- Command replies, button edits, and the final edit of an answer do not wait for the group
  budget.

In a busy group, the preview changes every few seconds. When Telegram tells the bot to wait,
the preview stops until the wait ends. Then the answer ends as one formatted message.

The final answer has these rules:

- The bot sends the final text as Telegram MarkdownV2.
- A text longer than 4096 characters goes out in parts, with `(1/3)`, `(2/3)`, and so on.
- If Telegram refuses the markup, the bot sends plain text.
- If the preview is older than 60 seconds, the final answer goes out as a new message.
- A tool round splits the answer into separate messages.
- When a turn uses all its tool rounds, a ⚠️ notice comes first. Then the summary of the work
  follows as the answer. See [One turn, step by step](How-It-Works#one-turn-step-by-step).

### Approval buttons

The agent asks you before it runs a dangerous command. The message shows a short summary when the
agent gives one, the reason for the flag, the command, and these buttons:

- ✅ Allow once
- ✅ Allow in this session
- ✅ Always allow
- ❌ Deny

No answer in 10 minutes counts as a denial. `approvals.timeout_s` sets this time. After the answer
or the timeout, the message shows the result, the summary, and the command in a closed quote.

A permission request of a coding agent (`acp_delegate`) has no **Always allow** button. It shows
these buttons:

- ✅ Allow once
- ✅ Allow in this session
- ❌ Deny

A request with no session answer shows only ✅ Allow once and ❌ Deny. An example is a compound
command. See [Security](Security#coding-agents) for the full rule.

You can also answer with a command:

- `/approve`, `/approve session`, or `/approve always`
- `/deny`
- Add `all` to answer all the waiting approvals.

On a coding agent request, `/approve always` gives `session`, or `once` when the request has no
session answer.

These commands do not go through the gateway queue, so they work also while a task waits for
the approval. See [Security](Security) for the approval rules.

Only an owner can answer an approval, with a button or with a command. A member gets
`⛔ Only the owner can answer this prompt.` Any owner can answer each approval. `/approve` and
`/deny` answer only the approvals of the same topic.

### While the agent is busy

The gateway runs one turn at a time for each session. A message that you send during a turn
does not start a second turn:

- A plain text goes into the running task as a steer. The bot replies "↪️ Got it. I'll do this
  after the current step."
- A photo, a file, or a voice note waits in the queue. The bot replies "⏳ Queued. I'll answer
  after the current task."
- A command that can change the session gets the "⏳ Busy" reply with a **⏹ Stop the task**
  button.

The steer reply and the queue reply have a **⚡ Do this now** button. A tap stops the task and
runs your waiting message at once, the same as `/now`. A button from an older turn stops
nothing.

The bot edits the steer reply when the steer is over: "✅ The agent read your note.",
"↪️ The task ended first. Your note runs as a new message.", or "⏹ Not sent. The task stopped."
It edits the queue reply to "▶️ The agent works on your message now." when your message starts,
or to "⏹ Not sent. The task stopped." When the gateway restarts, the replies change to
"↪️ Your note runs after the restart." and "⏳ Your message runs after the restart." The restart
keeps only the text of a message, so the reply of a photo or a file changes to "⏳ After the
restart, the agent gets your message without its photo or file." The button goes away with each
edit.

The bot sends a maximum of one steer reply and one queue reply for each session every 30 seconds
in one turn. After the bot edits a reply, the next message gets a new reply at once.

For `/stop`, `/stop keep`, `/now`, and `/steer`, see
[Commands](Commands#while-the-agent-is-busy).

### Questions from the agent

The agent can ask you a question with the `clarify` tool. The bot shows the choices as buttons.
You can also type the answer as the next message.

Only the user whose turn asked the question can answer it. A tap of another user gets
`⛔ This question is for another user.` A text of another user goes on as a normal message. A
question from a turn with no user, for example a goal turn, goes to any owner.

In an observed chat, all users share the session. The question still binds to the sender of
the turn that asked it, so a member answers the question of their own turn. Merged texts or
left-over texts of more than one sender have no user. Only an owner answers the question of
such a turn.

### The model list

`/model <provider>` shows the models of the entry as buttons:

```text
🧠 primary (OpenRouter) · 15 models
Most used this week (1 to 10), then best for agents (11 to 15). Only models with tool support.
[deepseek-v4.1-flash-1017] [glm-5.3-flash]
[deepseek-v4.1-flash]      [gpt-6-luna]
[hy4-preview]              [claude-opus-5.5]
[step-5-preview]           [minimax-m3.1-flash]
[·] [1 / 2] [Next ▶]
[↩ Providers] [↻ Refresh]
```

- A page has 8 models (`gateway.model_page_size`), 2 in a row. The row with ◀ Prev, `n / N`, and Next ▶ is the same as in
  the `/sessions` list. A list with one page has no such row.
- A button shows the part of the model ID after the last `/`. A label longer than 32
  characters ends in `…`.
- The ✓ marks the model that this session uses with the entry.
- A tap on a page button, ↻ Refresh, or ↩ Providers replaces the message. A tap on a model
  replaces the list with the confirmation.
- A model button sends the position of the model and a stamp of the list, not the model ID.
  Telegram permits only 64 bytes of button data.
- If the list changed after the bot sent the button, the reply is the new page 1 with the line
  `The list changed. Select the model again.` The model does not change.
- An entry name longer than 20 bytes gets no model buttons. Its list is a numbered text list
  with the line `Send /model <provider>:<model> to use a model.`
- During a task, a tap gets the busy reply, and the model does not change. Tap the button
  again after the task.

For the forms of the command, see [Commands](Commands#model-and-effort).

### Buttons and other users

Only the user who sent a command can tap its buttons. Another user sees "⛔ This button is for
another user." The buttons continue to work after a restart. The **⚡ Do this now** buttons of
the busy replies are the exception. The bot removes them when the gateway stops, and the replies
say that the message runs after the restart.

A tap on a command button runs the action of the button as a typed command. Thus the busy rule
applies to a button too. The **Resume** button of a goal notice starts the next goal turn.

A member tap on a button of an owner-only command gets `⛔ Only the owner can use /<word>.`
The bot does not show such buttons to a member.

## Media that you send

| Type | What happens |
|---|---|
| Photo | The model gets the largest size as an image. |
| Voice note | The bot transcribes it when speech-to-text is set up. Else the agent gets a note that transcription is not set up. |
| Document | The bot saves documents of up to 20 MB of these types: pdf, md, txt, csv, log, json, xml, yaml, yml, toml, ini, cfg, zip, docx, xlsx, pptx, ts, py, sh. A `.md` or `.txt` file of 100 KB (`telegram.inline_text_max_bytes`) or less goes into the message as text. |
| Album | The bot waits 800 ms (`telegram.album_wait_s`) and joins the parts into one message. |
| Sticker | The vision model describes a static sticker. |
| Video, audio, location, venue | The bot handles these types too. |
| Rich message | The agent gets the text as Markdown, with its headings, lists, tables, and code blocks. The bot reads the first 10 media files of one message or one album. The media of one rich message has a time limit of 2 minutes. The agent gets a note for each media file that the bot did not read. |

To set up speech-to-text, see [Tools](Tools#media).

## Media that the agent sends

The agent can send local files after its text:

- An answer can contain a `MEDIA:<path>` tag or a bare file path.
- The flag `[[audio_as_voice]]` sends audio as a voice note.
- The flag `[[as_document]]` sends a file as a document.

Text-to-speech audio becomes a voice note when `ffmpeg` is installed. Without `ffmpeg`, the bot
sends the audio as a file.

The bot never sends a protected path. The four `gateway.media_*` keys control the
other files that it can send. A `TETHER_MEDIA_*` variable that is set wins over its key. See
[docs/SECURITY.md](https://github.com/dnwandana/tether-agent/blob/main/docs/SECURITY.md) for the
full rule.

## See also

- [Commands](Commands): all slash commands and the busy behavior.
- [Cron](Cron): scheduled jobs that send results to Telegram.
- [Security](Security): the approval gate.
