# Configuration reference

Tether Agent reads all settings from one file: `~/.tether/config.yaml`. Put secrets in
`~/.tether/.env`, not in the config file. [SECURITY.md](SECURITY.md) tells how the program
keeps the secrets safe.

## How the file works

- Every key has a default. The tables below give each key, its default, and its effect.
- A missing file is valid, but `providers.primary` is necessary. Run `tether setup` to
  write the first file.
- Each string value can refer to an environment variable with `${VAR}`. The program reads the
  value from the process environment first, then from `.env`.
- A reference to a variable that is not set stops the load. The error names the variable and
  the key.
- An enum key with an unknown value stops the load. The error gives the permitted values.
- A config change applies after a restart. There are two exceptions:
  - The program reads the skills again at the start of each session.
  - The `/reload-mcp` command reloads the MCP servers.
- `--data-dir` or the `TETHER_HOME` environment variable moves the data folder from
  `~/.tether`.

In a key, `<name>` is a name that you choose. For example, `providers.<name>.model` can be
`providers.primary.model` or `providers.cheap.model`.

An empty string is `""`. An empty list is `[]`. An empty map is `{}`.

## providers

Each entry is one model endpoint. The entry `primary` is necessary. Other keys can refer to any
entry by its name. `agent.fallback` gives the entries that get a call when the entries before them
fail. "The fallback chain" below links to the rules for the next entry. An entry
with the name `fallback` is the old shape of `agent.fallback`: when `agent.fallback` is absent, the
agent uses that entry as the only fallback.

An entry with `preset` takes the values of a known endpoint from the table below:

- An empty `format` or `base_url` takes the value of the preset. A value that you set wins.
- An empty `api_key` takes the value of the key env of the preset. This occurs only when the
  `base_url` is the URL of the preset, so the key of a preset never goes to another host.
- A key env that is set but empty counts as not set.
- The URL compare ignores a trailing slash.
- The quirks and the effort style of the preset also apply only on the URL of the preset.
- The preset does not fill `model`. Each entry must set it.

An entry is not usable when it has no model. An entry with a preset is also not usable when it
has no API key. The entry `primary` that is not usable stops the load. Another entry that is not
usable loads, and the agent logs a warning for it. The fallback chain skips the entry, so no call
goes to it. `/model` refuses the entry and shows the cause.

An entry with no preset and no `api_key` loads. The agent sends no auth header to it, for example
to a local model server.

The key `flat_rate` is removed. The usage report counts every provider in dollars. The program
ignores the key, and `tether doctor` gives a warning for it.

| Wizard item | Preset | Format | Base URL | Key env | Quirks |
|---|---|---|---|---|---|
| xAI | `xai` | `responses` | `https://api.x.ai/v1` | `XAI_API_KEY` | `conv_id_header`, `prompt_cache_key` |
| OpenRouter | `openrouter` | `openai` | `https://openrouter.ai/api/v1` | `OPENROUTER_API_KEY` | `openrouter_headers` |
| OpenAI | `openai` | `responses` | `https://api.openai.com/v1` | `OPENAI_API_KEY` | `prompt_cache_key` |
| Anthropic | `anthropic` | `anthropic` | `https://api.anthropic.com` | `ANTHROPIC_API_KEY` | none |
| Z.AI, Pay as you go | `zai` | `openai` | `https://api.z.ai/api/paas/v4` | `ZAI_API_KEY` | `reasoning_content`, `keep_thinking` |
| Z.AI, Coding Plan | `zai-coding` | `openai` | `https://api.z.ai/api/coding/paas/v4` | `ZAI_API_KEY` | `reasoning_content` |
| DeepSeek | `deepseek` | `openai` | `https://api.deepseek.com/v1` | `DEEPSEEK_API_KEY` | `reasoning_content` |
| Moonshot | `moonshot` | `openai` | `https://api.moonshot.ai/v1` | `MOONSHOT_API_KEY` | `reasoning_content` |
| Alibaba | `alibaba` | `openai` | `https://dashscope-intl.aliyuncs.com/compatible-mode/v1` | `DASHSCOPE_API_KEY` | `reasoning_content` |
| MiniMax | `minimax` | `anthropic` | `https://api.minimax.io/anthropic` | `MINIMAX_API_KEY` | `bearer_auth` |
| Ollama | `ollama-cloud` | `openai` | `https://ollama.com/v1` | `OLLAMA_API_KEY` | none |

The quirks change the requests to one provider:

- `conv_id_header`: the Responses call sends the session id in the `x-grok-conv-id` header.
- `prompt_cache_key`: the Responses call sends the session id as `prompt_cache_key`. The
  provider uses the key for its prompt cache.
- `openrouter_headers`: the chat call and the model list call send `HTTP-Referer` and `X-Title`.
  These headers name Tether Agent to OpenRouter.
- `reasoning_content`: the adapter reads the reasoning text from the `reasoning_content` field of
  the stream. It sends the field back on each assistant message. The value is the stored text
  when the same endpoint and model wrote it, else one space.
- `keep_thinking`: the request sends `thinking` with `clear_thinking: false`, so Z.AI keeps the
  thinking of earlier turns.
- `bearer_auth`: the calls of the `anthropic` format send the key as `Authorization: Bearer
  <key>`, not as `x-api-key`.

The Ollama preset is Ollama Cloud. For a local model server, for example Ollama, LM Studio, or
llama.cpp, use an entry with no preset: set `format: openai` and a `base_url` such as
`http://localhost:11434/v1`. The preset `ollama` is removed, and a config that uses it does
not load. The error tells what to set in its place.

| Key | Default | Effect |
|---|---|---|
| `providers.<name>.preset` | `""` | The id of a preset from the table above. It fills `format`, `base_url`, the key env, and the quirks. `""` is an entry with no preset. An unknown id stops the load, and the error gives the known ids. |
| `providers.<name>.format` | `""` | The wire format of the endpoint: `openai`, `anthropic`, or `responses`. It is necessary only for an entry with no preset. |
| `providers.<name>.base_url` | `""` | The base URL of the API, for example `https://openrouter.ai/api/v1`. With `format: anthropic`, the chat call goes to `<base_url>/v1/messages`, or to `<base_url>/messages` when the URL ends in `/v1`. The model list call uses the same rule. |
| `providers.<name>.api_key` | `""` | The API key. Use `${VAR}` and keep the value in `.env`. |
| `providers.<name>.model` | `""` | The model name that the agent sends to the endpoint. |
| `providers.<name>.vision` | `false` | When `true`, images go inline to this model. When `false`, the `view_image` tool uses `agent.vision_provider`. |
| `providers.<name>.context_window` | `0` | The context window in tokens. `0` takes the value from the price data. When the price data has no value, the agent uses 128000. |
| `providers.<name>.price_key` | `""` | The LiteLLM price key, for example `zai/glm-5.3`. Set it when the automatic key match fails. |
| `providers.<name>.effort` | `""` | The reasoning effort of this entry: `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, or `max`. `""` sends no effort field, so the provider uses its default. The `/effort` command of a session wins. Each level goes to the nearest level that the provider accepts. The effort style of a preset applies only on the preset URL. Else the entry uses the standard style of its `format`. When the entry rejects the effort with an HTTP 400 that names `effort`, `reasoning`, or `thinking`, the chain sends the call again with no effort. When that call works, the entry gets no effort until a restart. |

### The fallback chain

Each model call goes through a chain of entries: the pinned entry, then `primary`, then the
entries of `agent.fallback`. The wiki page
[The fallback chain](https://github.com/dnwandana/tether-agent/wiki/Providers#the-fallback-chain)
gives the order, the failures that move a call to the next entry, and the wait times.

## agent

| Key | Default | Effect |
|---|---|---|
| `agent.max_turns` | `90` | The maximum number of tool rounds in one turn. After the last round, the model writes a summary. |
| `agent.tool_concurrency` | `4` | The maximum number of tool calls that run at the same time in one round. |
| `agent.vision_provider` | `""` | The provider entry for `view_image`. `""` uses the default chain when `primary` has `vision: true`. Else the agent has no `view_image` tool. `primary` uses the default chain. In the default chain, the call goes only to the entries with `vision: true`. Another name uses that entry alone, with no fallback. |
| `agent.delegation_provider` | `""` | The provider for child runs of `delegate_task`, as `name` or `name:model`. The value is the pin of the child session, so that entry is first in the chain of the child. `""` uses the pin of the parent session. |
| `agent.restart_drain_timeout_s` | `180` | The maximum time in seconds that a stop or a restart waits for the running turns. The turns that still run after it are cancelled, and they continue after the restart. `tether setup` writes `TimeoutStopSec` from this value. |
| `agent.fallback` | `[]` | The ordered list of entry names after `primary` in the chain (see "The fallback chain"). Each name must be an entry in `providers`. The list must not contain `primary` or a name two times. The default is no key. With no key, the list is the entry `fallback` when it exists, else there is no fallback. An explicit `[]` turns off the fallback. |
| `agent.resume_freshness_s` | `3600` | A turn that a restart or a crash cut continues at the next start when the cut is not older than this time, in seconds. `0` turns off the resume. An active goal also continues only within this time. Else it pauses. `0` pauses each goal. |

## context

| Key | Default | Effect |
|---|---|---|
| `context.compact_threshold` | `0.5` | Compaction starts when the context is above this part of the context window. |
| `context.protect_first_n` | `3` | Compaction keeps this number of messages at the start of the conversation. |
| `context.protect_last_n` | `20` | Compaction keeps this number of messages at the end of the conversation. |
| `context.summary_target_ratio` | `0.2` | The target size of the compaction summary, as a part of the trigger size. |
| `context.tool_output_max_chars` | `50000` | A longer tool output keeps its head and its tail. The full output goes to a spill file in `cache/spill/`. |
| `context.spill_retention_days` | `7` | The program deletes spill files that are older than this number of days. |

## sessions

| Key | Default | Effect |
|---|---|---|
| `sessions.retention_days` | `90` | The gateway deletes an automatic session when its last activity is older than this number of days. `0` keeps all sessions. |

The gateway deletes old automatic sessions when it starts, and then one time each day. These
kinds of session are automatic:

- `cron`: one session for each cron run.
- `subagent`: one session for each `delegate_task` child.
- `review`: one session for each background review.
- `goal_judge`: one session for the judge calls and the draft calls of each goal.

The deletion also removes the messages, the search index rows, the compaction rows, the key
mappings, the goal rows, and the finished delegation batches of the session. Chat sessions from
the CLI and Telegram always stay. A session that a deleted session started, for example a fork,
stays with no parent.

## memory

| Key | Default | Effect |
|---|---|---|
| `memory.memory_char_limit` | `2200` | The maximum size of `memory/memory.md`, in characters. |
| `memory.user_char_limit` | `1375` | The maximum size of `memory/user_profile.md`, in characters. |
| `memory.review_interval_turns` | `10` | A background memory review starts after this number of user turns. `0` stops the reviews. |

## skills

| Key | Default | Effect |
|---|---|---|
| `skills.external_dirs` | `[]` | More skill folders. The agent reads them but never writes to them. |
| `skills.review_interval_tool_rounds` | `10` | A background skill review starts after this number of tool rounds. `0` stops the reviews. |
| `skills.guard` | `true` | When `true`, the skill guard scans agent-written skills and skills in external folders. |

## web

| Key | Default | Effect |
|---|---|---|
| `web.search_backend` | `duckduckgo` | The backend of `web_search`. v1 has only `duckduckgo`. |
| `web.fetch_max_urls` | `5` | The maximum number of URLs in one `web_fetch` call. |
| `web.fetch_max_bytes` | `750000` | The maximum size of one page that `web_fetch` downloads. |
| `web.block_private_networks` | `true` | When `true`, the web tools refuse loopback, private, link-local, and CGNAT addresses. |

## browser

| Key | Default | Effect |
|---|---|---|
| `browser.chrome_path` | `""` | The path of the Chrome program. `""` uses the `CHROME_PATH` environment variable, then finds Chrome or Chromium in `PATH`. Then it tries `/snap/bin/chromium` on Linux, and the Chrome and Chromium apps on macOS. |
| `browser.idle_close_minutes` | `10` | The program stops Chrome after this number of minutes with no browser call. |

## terminal

| Key | Default | Effect |
|---|---|---|
| `terminal.default_timeout_s` | `120` | The default timeout of the `terminal` tool, in seconds. |
| `terminal.env_passthrough` | `[]` | Names of secret environment variables that child processes can see. A secret that a skill needs passes only through this list. |

## code_execution

| Key | Default | Effect |
|---|---|---|
| `code_execution.timeout_s` | `300` | The timeout of one `execute_code` call, in seconds. |
| `code_execution.max_tool_calls` | `50` | The maximum number of tool calls from one `execute_code` call. |
| `code_execution.max_stdout_bytes` | `50000` | The maximum stdout of one `execute_code` call that goes back to the model. |

## delegation

| Key | Default | Effect |
|---|---|---|
| `delegation.max_concurrent_children` | `5` | The maximum number of child runs at the same time. |
| `delegation.max_spawn_depth` | `1` | The maximum depth of child runs. With `1`, a child cannot start a child. |
| `delegation.max_iterations` | `50` | The maximum number of tool rounds in one child run. |
| `delegation.child_timeout_s` | `600` | The timeout of one child run, in seconds. |

## approvals

| Key | Default | Effect |
|---|---|---|
| `approvals.mode` | `manual` | The approval mode for dangerous commands: `manual`, `smart`, or `off`. |
| `approvals.timeout_s` | `600` | The time that the gate waits for an answer, in seconds. No answer is a denial. It also ends the confirm of a dangerous goal gate. |
| `approvals.cron_mode` | `deny` | The result for a dangerous command in a cron run: `deny` or `approve`. |

## goals

| Key | Default | Effect |
|---|---|---|
| `goals.max_turns` | `20` | The turn budget of a new goal. When the goal uses all its turns, it pauses. |
| `goals.judge_provider` | `""` | The provider entry of the goal judge and of `/goal draft`. The chain is the entry, then `primary`, then `agent.fallback`. `""` uses the chain of the session. The name must be an entry in `providers`. |
| `goals.judge_timeout_s` | `30` | The timeout of one judge call or draft call, in seconds. |

## toolsets

| Key | Default | Effect |
|---|---|---|
| `toolsets.<name>` | `[all]` | The toolsets that the channel `<name>` can use. The channels are `cli`, `telegram`, and `cron`. The value `all` gives every toolset. |

The default is `cli: [all]`, `telegram: [all]`, and `cron: [all]`. A cron job or a child run can
only make its channel set smaller.

## cron

| Key | Default | Effect |
|---|---|---|
| `cron.max_parallel_jobs` | `2` | The maximum number of cron jobs that run at the same time. More due jobs wait for the next tick. |
| `cron.missed_runs` | `skip` | The rule for a run that the program missed while it was stopped: `skip` or `catchup`. |

The gateway applies `cron.missed_runs` when it starts. The rules are:

- `skip`: a recurring job that is late by less than its grace window still runs one time. The
  grace window is half the period of the schedule, with a minimum of 2 minutes and a maximum of
  2 hours. For example, an `every 30m` job has a grace window of 15 minutes, and a daily job has
  a grace window of 2 hours. A job that is later than its grace window does not run. It moves to
  its next scheduled time after the start.
- `catchup`: each recurring job that is due runs one time, also when it missed more than one run.
- A one-shot job always runs one time when it is late, because it has no next run. This applies
  to the two rules.

## telegram

| Key | Default | Effect |
|---|---|---|
| `telegram.enabled` | `false` | When `true`, the gateway runs the Telegram channel. |
| `telegram.bot_token` | `""` | The bot token. When the file does not set it, the program uses `${TELEGRAM_BOT_TOKEN}`. |
| `telegram.allowed_users` | `[]` | User ids that can use the bot. `*` permits all users. |
| `telegram.allow_all_users` | `false` | When `true`, every user can use the bot. Use it only for a test bot. |
| `telegram.group_allowed_users` | `[]` | User ids that can use the bot in groups. This list does not permit direct messages. A user in this list is an owner in a group. |
| `telegram.group_allowed_chats` | `[]` | Group chat ids where each member can talk to the bot. `*` permits all groups. Only an owner can answer an approval and run the owner commands. Add a trusted member to `group_allowed_users`. |
| `telegram.allowed_chats` | `[]` | When set, the bot answers only in these group chats. `[]` sets no chat limit. |
| `telegram.free_response_chats` | `[]` | Group chats where the bot answers every message, with no mention. |
| `telegram.require_mention` | `false` | When `true`, the bot answers in a group only to a mention, a reply, or a mention pattern. |
| `telegram.exclusive_bot_mentions` | `true` | When `true`, the bot ignores a group message that mentions only another bot. |
| `telegram.mention_patterns` | `[]` | Regular expressions. A match in the text counts as a mention. The match ignores case. |
| `telegram.guest_mode` | `false` | When `true`, the bot answers a direct mention in a chat that `allowed_chats` does not list. |
| `telegram.allowed_topics` | `[]` | When set, the bot answers only in these forum topics. A message with no topic id counts as topic `1`. |
| `telegram.ignored_threads` | `[]` | The bot ignores the messages in these threads. |
| `telegram.group_sessions_per_user` | `true` | When `true`, each user in a group without topics has a separate session. |
| `telegram.thread_sessions_per_user` | `false` | When `true`, each user in a forum topic has a separate session. |
| `telegram.observe_unmentioned_group_messages` | `false` | When `true`, the bot saves group messages that it does not answer as context. This applies only in chats that `group_allowed_chats` lists. |
| `telegram.unauthorized_dm_behavior` | `""` | The reply to a direct message from an unknown user: `pair` or `ignore`. `""` gives `ignore` when an allowlist is set, else `pair`. |
| `telegram.home_chat` | `""` | The first home chat. The `/sethome` command saves a newer value in `state.db`. |
| `telegram.home_thread` | `""` | The first home topic. The `/sethome` command saves a newer value in `state.db`. |
| `telegram.cron_thread` | `""` | The topic for cron delivery to the home chat. It overrides `home_thread` for cron only. |
| `telegram.streaming.transport` | `auto` | The streaming method: `auto`, `edit`, or `draft`. `auto` and `draft` use drafts in direct messages and edits in groups. |
| `telegram.streaming.edit_interval_s` | `0.8` | The minimum time between two edits of a streamed answer, in seconds. |
| `telegram.streaming.buffer_threshold` | `24` | An edit also occurs when this number of new characters is in the buffer. |
| `telegram.streaming.cursor` | `" ▉"` | The text at the end of each edit while the answer streams. |
| `telegram.streaming.fresh_final_after_s` | `60` | When the preview message is older than this number of seconds, the final answer goes out as a new message. |
| `telegram.tool_progress` | `all` | The tool status lines: `off`, `new` (a line when the tool changes), `all` (one line per tool, with a count), or `verbose` (each call, with its arguments). With `off`, a goal turn still sends its goal line as one silent message. No plan message goes out. |
| `telegram.cleanup_progress` | `false` | When `true`, the bot deletes the status message after a successful run. The goal message and the plan messages go too, but the "Plan done" message stays. In `off` mode, the goal message stays. |

Four environment variables control the local files that an answer can send. Set them in `.env`
or in the process environment. `docs/SECURITY.md` ("Files that the bot sends") gives the full rule.

- `TETHER_MEDIA_DELIVERY_STRICT=1` turns on strict mode.
- `TETHER_MEDIA_ALLOW_DIRS` lists more folders that the bot can send from, separated by `:` or `,`.
- `TETHER_MEDIA_TRUST_RECENT_FILES=0` stops strict mode from sending recently changed files.
- `TETHER_MEDIA_TRUST_RECENT_SECONDS` sets the recency window of strict mode. The default is 600.

## mcp

Each entry of `mcp.servers` is one MCP server. Set `command` for a stdio server, or set `url` for
an HTTP server. A key that the entry does not set keeps the default in this table.

The program changes each character of a server name that is not a letter, a digit, `_`, or `-` to
`_`. Two server names must stay different after this change, also when the case is ignored. For
example, `a b` and `a_b`, or `GitHub` and `github`, are not valid together.

| Key | Default | Effect |
|---|---|---|
| `mcp.servers.<name>.command` | `[]` | The program and its arguments for a stdio server, for example `[npx, -y, some-mcp-server]`. |
| `mcp.servers.<name>.env` | `{}` | Environment variables for a stdio server. These variables go to the server, also when they are secrets. |
| `mcp.servers.<name>.url` | `""` | The URL of an HTTP server. |
| `mcp.servers.<name>.transport` | `""` | The HTTP transport. `""` uses streamable HTTP. `sse` uses server-sent events. |
| `mcp.servers.<name>.headers` | `{}` | HTTP headers for an HTTP server, for example an authorization header. |
| `mcp.servers.<name>.timeout_s` | `120` | The timeout of one tool call, in seconds. |
| `mcp.servers.<name>.connect_timeout_s` | `60` | The timeout of the connection, in seconds. |
| `mcp.servers.<name>.supports_parallel_tool_calls` | `false` | When `true`, calls to this server can run at the same time. |
| `mcp.servers.<name>.always_on` | `false` | When `true`, the server starts at startup and stays connected. When `false`, it starts at the first call. |
| `mcp.servers.<name>.idle_stop_minutes` | `10` | The program stops the server after this number of minutes with no call. |
| `mcp.servers.<name>.enabled` | `true` | When `false`, the program ignores the server. |

## acp

The `acp` block sets the `acp_delegate` tool. The tool gives a coding task to an external
coding agent, for example Claude Code, over the Agent Client Protocol (ACP). Each entry of
`acp.agents` is one agent. An entry turns on its agent. A key that the entry does not set keeps
the default in this table. An unknown key in an entry is an error.

`claude-code` and `codex` are built-in agents. For these two ids, an empty `command` and an
empty `modes` get the built-in values. Another id must set `command` and `modes`.

| Key | Default | Effect |
|---|---|---|
| `acp.enabled` | `false` | When `true`, the program registers the `acp_delegate` tool. At least one agent must be enabled. |
| `acp.idle_timeout_s` | `300` | The program stops an adapter process after this number of seconds with no task. The minimum is 60. The next call resumes the session of the coding agent. |
| `acp.max_sessions` | `2` | The maximum number of live adapter processes in one Tether Agent process. The gateway and each `tether chat` process have their own limit. The minimum is 1. |
| `acp.turn_timeout_s` | `1800` | The maximum time of one `acp_delegate` call, in seconds. The `timeout_s` argument of the tool can only make it shorter. The minimum is 60. |
| `acp.workdir_allowlist` | `[]` | The folders where a coding agent can work. Each entry must be an absolute path. A leading `~/` expands to the home folder. With `[]`, the tool refuses each call. |
| `acp.agents.<name>.enabled` | `true` | When `false`, the program ignores the agent. |
| `acp.agents.<name>.command` | `[]` | The adapter program and its arguments, for example `[npx, -y, @agentclientprotocol/claude-agent-acp]`. `[]` uses `[claude-agent-acp]` for `claude-code` and `[codex-acp]` for `codex`. When the program is an absolute path, its folder goes first in the `PATH` of the adapter. |
| `acp.agents.<name>.env` | `{}` | More environment variables for the adapter. A value replaces the inherited value. A secret name is an error: a name that ends in `_KEY`, `_TOKEN`, or `_SECRET`, or that contains `PASSWORD`. |
| `acp.agents.<name>.permission_preset` | `auto` | The permission preset: `all`, `risky`, `auto`, or `folder`, from strict to loose. The model can choose this preset or a stricter one. |
| `acp.agents.<name>.model` | `""` | The model of the coding agent. `""` uses the default of the adapter. |
| `acp.agents.<name>.version_pin` | `""` | The expected version of the adapter. `tether doctor` warns when the version is different. It does not stop a start. |
| `acp.agents.<name>.modes` | `{}` | The mode id of the agent for each preset. `{}` uses the built-in modes of `claude-code` and `codex`. |

The built-in modes:

| Preset | `claude-code` mode | `codex` mode |
|---|---|---|
| all | `default` | `read-only` |
| risky | `acceptEdits` | `workspace-write` |
| auto | `auto` | `agent` |
| folder | `bypassPermissions` | `agent-full-access` |

## media

Each media tool has a backend. `command` runs a command template on the host. `endpoint` sends
the request to an OpenAI-format endpoint. `""` hides the tool.

The command templates can use the placeholders `{text}`, `{in}`, `{out}`, `{out_base}`,
`{prompt}`, and `{size}`. The program never runs a template through a shell.
When a template word is only `{text}` or `{prompt}` and the value starts with `-`,
the program puts one space before the value. Thus the program does not read the value as an option.

| Key | Default | Effect |
|---|---|---|
| `media.tts.backend` | `command` | The backend of `text_to_speech`: `command`, `endpoint`, or `""`. |
| `media.tts.command` | `edge-tts --voice en-US-AriaNeural --text {text} --write-media {out}` | The command template of `text_to_speech`. |
| `media.tts.provider` | `""` | The provider entry for the `endpoint` backend of `text_to_speech`. `""` uses `primary`. |
| `media.stt.backend` | `command` | The backend of `transcribe_audio`: `command`, `endpoint`, or `""`. |
| `media.stt.command` | `whisper-cli -m ~/models/ggml-base.bin -f {in} -otxt -of {out_base}` | The command template of `transcribe_audio`. |
| `media.stt.provider` | `""` | The provider entry for the `endpoint` backend of `transcribe_audio`. `""` uses `primary`. |
| `media.image.backend` | `""` | The backend of `generate_image`. `""` hides the tool. |
| `media.image.command` | `""` | The command template of `generate_image`. |
| `media.image.provider` | `""` | The provider entry for the `endpoint` backend of `generate_image`. `""` uses `primary`. |
| `media.cache_retention_days` | `7` | The gateway deletes a file in `cache/media/` and `cache/screenshots/` when its last change is older than this number of days. The check runs at start and each 24 hours. `0` keeps all files. A negative value is a config error. `tether chat` alone deletes no files. |

## pricing

| Key | Default | Effect |
|---|---|---|
| `pricing.sources` | `[openrouter, litellm]` | The price data sources. `[]` stops all price downloads. |
| `pricing.refresh_hours` | `24` | The program downloads the price data again after this number of hours. |
| `pricing.overrides.<name>.input` | `0` | A manual input price for the model `<name>`, in USD for 1M tokens. |
| `pricing.overrides.<name>.output` | `0` | A manual output price for the model `<name>`, in USD for 1M tokens. |
| `pricing.overrides.<name>.cache_read` | `0` | A manual cache read price for the model `<name>`, in USD for 1M tokens. |

An override applies to a provider when `<name>` is its `price_key` or its `model`.

## log

| Key | Default | Effect |
|---|---|---|
| `log.level` | `info` | The log level: `debug`, `info`, `warn`, or `error`. The `--log-level` flag overrides it. |
| `log.max_size_mb` | `10` | The log file rotates at this size, in MB. |
| `log.max_files` | `3` | The number of log files that the program keeps. |
