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

Each entry is one model endpoint. The entry `primary` is necessary. The entry `fallback` is
optional: the agent uses it when the primary provider fails before the first byte of an answer.
Other keys can refer to any entry by its name.

| Key | Default | Effect |
|---|---|---|
| `providers.<name>.format` | `""` | The wire format of the endpoint: `openai` or `anthropic`. Each entry must set it. |
| `providers.<name>.base_url` | `""` | The base URL of the API, for example `https://openrouter.ai/api/v1`. |
| `providers.<name>.api_key` | `""` | The API key. Use `${VAR}` and keep the value in `.env`. |
| `providers.<name>.model` | `""` | The model name that the agent sends to the endpoint. |
| `providers.<name>.flat_rate` | `false` | When `true`, the usage report shows no cost for this provider, because you pay a flat rate. |
| `providers.<name>.vision` | `false` | When `true`, images go inline to this model. When `false`, the `view_image` tool uses `agent.vision_provider`. |
| `providers.<name>.context_window` | `0` | The context window in tokens. `0` takes the value from the price data. When the price data has no value, the agent uses 128000. |
| `providers.<name>.price_key` | `""` | The LiteLLM price key, for example `zai/glm-4.7`. Set it when the automatic key match fails. |

## agent

| Key | Default | Effect |
|---|---|---|
| `agent.max_turns` | `40` | The maximum number of tool rounds in one turn. |
| `agent.tool_concurrency` | `4` | The maximum number of tool calls that run at the same time in one round. |
| `agent.vision_provider` | `""` | The provider for `view_image`. `""` uses the primary provider when it has `vision: true`. Else the agent has no `view_image` tool. |
| `agent.delegation_provider` | `""` | The provider for child runs of `delegate_task`. `""` uses the provider of the parent. |

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

The deletion also removes the messages, the search index rows, the compaction rows, the key
mappings, and the finished delegation batches of the session. Chat sessions from the CLI and
Telegram always stay. A session that a deleted session started, for example a fork, stays with
no parent.

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
| `browser.chrome_path` | `""` | The path of the Chrome program. `""` uses the `CHROME_PATH` environment variable, then finds Chrome in `PATH`. |
| `browser.idle_close_minutes` | `10` | The program stops Chrome after this number of minutes with no browser call. |

## terminal

| Key | Default | Effect |
|---|---|---|
| `terminal.default_timeout_s` | `120` | The default timeout of `run_command`, in seconds. |
| `terminal.env_passthrough` | `[]` | Names of secret environment variables that child processes can see. |

## code_execution

| Key | Default | Effect |
|---|---|---|
| `code_execution.timeout_s` | `300` | The timeout of one `run_script` call, in seconds. |
| `code_execution.max_tool_calls` | `50` | The maximum number of tool calls from one `run_script` call. |
| `code_execution.max_stdout_bytes` | `50000` | The maximum stdout of one `run_script` call that goes back to the model. |

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
| `approvals.timeout_s` | `600` | The time that the gate waits for an answer, in seconds. No answer is a denial. |
| `approvals.cron_mode` | `deny` | The result for a dangerous command in a cron run: `deny` or `approve`. |

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
| `telegram.group_allowed_users` | `[]` | User ids that can use the bot in groups. This list does not permit direct messages. |
| `telegram.group_allowed_chats` | `[]` | Group chat ids where every member can use the bot. `*` permits all groups. |
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
| `telegram.tool_progress` | `all` | The tool status lines: `off`, `new`, `all`, or `verbose`. |
| `telegram.cleanup_progress` | `false` | When `true`, the bot deletes the status message after a successful run. |

Four environment variables control the local files that an answer can send. Set them in `.env`
or in the process environment. `docs/SECURITY.md` ("Files that the bot sends") gives the full rule.

- `TETHER_MEDIA_DELIVERY_STRICT=1` turns on strict mode.
- `TETHER_MEDIA_ALLOW_DIRS` lists more folders that the bot can send from, separated by `:` or `,`.
- `TETHER_MEDIA_TRUST_RECENT_FILES=0` stops strict mode from sending recently changed files.
- `TETHER_MEDIA_TRUST_RECENT_SECONDS` sets the recency window of strict mode. The default is 600.

## mcp

Each entry of `mcp.servers` is one MCP server. Set `command` for a stdio server, or set `url` for
an HTTP server. A key that the entry does not set keeps the default in this table.

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

## media

Each media tool has a backend. `command` runs a command template on the host. `endpoint` sends
the request to an OpenAI-format endpoint. `""` hides the tool.

The command templates can use the placeholders `{text}`, `{in}`, `{out}`, `{out_base}`,
`{prompt}`, and `{size}`. The program never runs a template through a shell.

| Key | Default | Effect |
|---|---|---|
| `media.tts.backend` | `command` | The backend of `text_to_speech`: `command`, `endpoint`, or `""`. |
| `media.tts.command` | `edge-tts --voice en-US-AriaNeural --text {text} --write-media {out}` | The command template of `text_to_speech`. |
| `media.tts.provider` | `""` | The provider entry for the `endpoint` backend of `text_to_speech`. |
| `media.stt.backend` | `command` | The backend of `transcribe_audio`: `command`, `endpoint`, or `""`. |
| `media.stt.command` | `whisper-cli -m ~/models/ggml-base.bin -f {in} -otxt -of {out_base}` | The command template of `transcribe_audio`. |
| `media.stt.provider` | `""` | The provider entry for the `endpoint` backend of `transcribe_audio`. |
| `media.image.backend` | `""` | The backend of `image_generate`. `""` hides the tool. |
| `media.image.command` | `""` | The command template of `image_generate`. |
| `media.image.provider` | `""` | The provider entry for the `endpoint` backend of `image_generate`. |

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
