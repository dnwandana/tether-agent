# Configuration

Tether Agent reads all settings from one YAML file, `config.yaml`, in the data folder. This page
explains how the file works and gives the settings that most users change. The full list of keys
and defaults is in
[docs/CONFIG.md](https://github.com/dnwandana/tether-agent/blob/main/docs/CONFIG.md).

`tether setup` writes the first version of the file. See [Getting Started](Getting-Started).

## Files

The data folder is `~/.tether` by default. The `--data-dir` flag or the `TETHER_HOME`
environment variable moves it. The order is `--data-dir`, then `TETHER_HOME`, then `~/.tether`.

| File | Content |
|---|---|
| `config.yaml` | All settings. Keep no secrets in this file. |
| `.env` | API keys, the bot token, and other secrets, as `NAME=value` lines. |

`tether setup` writes the two files with the mode `0600`. Before it changes a file, it saves a
`.bak` copy.

`.env` must have the mode `0600`. The program does not start when the group or other users have
a permission on the file. To fix it, run this command:

```sh
chmod 600 ~/.tether/.env
```

The agent tools cannot write `config.yaml` or `.env`. See [Security](Security#protected-paths).

## How the file works

- Every key has a default. A key that you do not set keeps its default.
- A missing file is valid, but `providers.primary` is necessary. Without it, `tether chat` and
  `tether gateway` stop with an error that tells you to run `tether setup`.
- A string value can refer to a variable with `${NAME}`. The program reads the value from the
  process environment first, then from `.env`.
- A `${NAME}` that is not set stops the load. The error names the variable and the key.
- An enum key with an unknown value stops the load. The error gives the permitted values.
- A value of the wrong type stops the load.
- An unknown key does not stop the load. The program ignores it, so check the spelling of each key.
- `telegram.bot_token` is a special case. When the file does not set it, the program uses
  `$TELEGRAM_BOT_TOKEN`.

### When a change applies

A config change applies after a restart of `tether chat` or `tether gateway`. There are two
exceptions:

- The program reads the skills again at the start of each session. Send `/new` to use a new
  skill.
- The `/reload-mcp` command reloads the MCP servers with no restart.

To restart the gateway, send `/restart` in a chat, or run `systemctl --user reload tether`. See
[Running as a Service](Running-as-a-Service).

## Sections

| Section | What it controls |
|---|---|
| `providers` | The model endpoints: the primary provider and other named entries. See [Providers](Providers). |
| `agent` | The turn loop: the tool round limit, parallel tools, the vision and delegation providers, the fallback list, the restart drain, and the resume of cut turns. |
| `context` | Compaction and the size limit of tool output. |
| `sessions` | How long the automatic sessions stay in the database. |
| `memory` | The size limits of the two memory files and the memory review interval. |
| `skills` | More skill folders, the skill review interval, and the skill guard. |
| `web` | `web_search` and `web_fetch`: the backend, the URL limits, and the private network block. |
| `browser` | The Chrome path and the idle stop time. |
| `terminal` | The command timeout and the secrets that child processes can see. |
| `code_execution` | The limits of `execute_code`. |
| `delegation` | The limits of `delegate_task` child runs. |
| `approvals` | The approval mode for dangerous commands, in chats and in cron runs. |
| `toolsets` | The toolsets that each channel can use. |
| `cron` | Parallel cron jobs and the rule for missed runs. |
| `telegram` | The bot: who can use it, group rules, home chat, streaming, and status lines. |
| `mcp` | The MCP servers. |
| `media` | The backends of text to speech, speech to text, and image generation. |
| `pricing` | The price data sources and manual prices. |
| `log` | The log level and the log file rotation. |

## Providers

A provider entry is one model endpoint. The entry `primary` is necessary. It is the primary
provider. You can add more entries with names that you choose. The [Providers](Providers) page
gives the presets, the wire formats, the API keys, the fallback chain, and the reasoning effort.

Each entry has these keys:

| Key | Default | Effect |
|---|---|---|
| `preset` | `""` | The id of a preset, for example `openrouter`. It fills `format`, `base_url`, and the API key. |
| `format` | `""` | The wire format: `openai`, `anthropic`, or `responses`. An entry with no preset must set it. |
| `base_url` | `""` | The base URL of the API. |
| `api_key` | `""` | The API key. Use `${NAME}` and keep the value in `.env`. |
| `model` | `""` | The model name that the agent sends. Each entry must set it. |
| `vision` | `false` | When `true`, images go inline to this model. |
| `context_window` | `0` | The context window in tokens. `0` takes the value from the price data, else 128000. |
| `price_key` | `""` | The LiteLLM price key. Set it when the automatic price match fails. |
| `effort` | `""` | The reasoning effort: `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, or `max`. `""` sends no effort field. The `/effort` level of a session wins. |

A `primary` entry that is not usable stops the load. For example, an entry with no model or a
preset entry with no API key is not usable. Another entry that is not usable loads, but the
fallback chain skips it.

This example has a primary provider and a fallback chain of two entries:

```yaml
providers:
  primary:
    preset: anthropic
    model: claude-sonnet-5-5
  deepseek:
    preset: deepseek
    model: deepseek-v4-pro
  openrouter:
    preset: openrouter
    model: z-ai/glm-5.3
agent:
  fallback: [deepseek, openrouter]
```

```sh
# ~/.tether/.env
ANTHROPIC_API_KEY=...
DEEPSEEK_API_KEY=sk-...
OPENROUTER_API_KEY=sk-or-...
```

A preset entry with no `api_key` reads the key env of the preset, for example
`ANTHROPIC_API_KEY`. The fallback chain goes to the next entry only when a call fails before the
first byte of the answer. See [Providers](Providers#the-fallback-chain).

## Important keys

These keys are the ones that most users change. The full list is in
[docs/CONFIG.md](https://github.com/dnwandana/tether-agent/blob/main/docs/CONFIG.md).

| Key | Default | Effect |
|---|---|---|
| `providers.primary.*` | `""` | The main model endpoint. Necessary. |
| `agent.fallback` | absent | The ordered list of entries for an outage of the primary provider. When the key is absent, the list is the entry `fallback`, if it exists. `[]` turns off the fallback chain. |
| `agent.max_turns` | `90` | The maximum number of tool rounds in one turn. After the last round, the model writes a summary. |
| `agent.tool_concurrency` | `4` | The maximum number of tool calls that run at the same time in one round. |
| `agent.delegation_provider` | `""` | The provider for child runs. `""` uses the provider of the parent. |
| `agent.vision_provider` | `""` | The provider for `view_image`. `""` uses `primary` when it has `vision: true`. |
| `agent.restart_drain_timeout_s` | `180` | The maximum time in seconds that a stop or a restart waits for the running turns. |
| `agent.resume_freshness_s` | `3600` | A turn that a restart or a crash cut continues at the next start when the cut is not older than this time, in seconds. `0` turns off the resume. |
| `context.compact_threshold` | `0.5` | Compaction starts when the context is above this part of the context window. |
| `context.tool_output_max_chars` | `50000` | A longer tool output keeps its head and tail. The full output goes to a spill file. |
| `memory.review_interval_turns` | `10` | A background memory review starts after this number of user turns. `0` stops the reviews. |
| `approvals.mode` | `manual` | The approval mode for dangerous commands: `manual`, `smart`, or `off`. |
| `approvals.cron_mode` | `deny` | The result for a dangerous command in a cron run: `deny` or `approve`. |
| `toolsets.<channel>` | `[all]` | The toolsets for the channels `cli`, `telegram`, and `cron`. |
| `telegram.enabled` | `false` | When `true`, the gateway runs the Telegram channel. |
| `telegram.allowed_users` | `[]` | The Telegram user ids that can use the bot. |
| `sessions.retention_days` | `90` | The gateway deletes cron, subagent, and review sessions after this number of days. Chat sessions always stay. |
| `log.level` | `info` | The log level: `debug`, `info`, `warn`, or `error`. |

## Toolsets per channel

A toolset is a group of tools, for example `web` or `browser`. The [Tools](Tools) page lists all
toolsets. Each channel has a list of toolsets:

- `cli`: the `tether chat` terminal chat.
- `telegram`: the Telegram bot.
- `cron`: the scheduled jobs.

The default for each channel is `[all]`. A hidden tool is removed from the request, so the model
does not see it. A cron job or a child run can make its channel list smaller, but it cannot add a
toolset.

```yaml
toolsets:
  cli: [all]
  telegram: [web, file, memory, skills, ask]
  cron: [web, file, terminal]
```

## Common recipes

### Use a cheaper model for subagents

Add a `cheap` entry under your existing `providers:` block, next to `primary`. Then set
`agent.delegation_provider` to its name. Do not add a second `providers:` key, because YAML
does not allow a duplicate key.

```yaml
providers:
  cheap:
    preset: openrouter
    model: <small model>
agent:
  delegation_provider: cheap
```

### Turn on smart approvals

In `smart` mode, the primary model judges each dangerous command. It approves a safe command. It
sends a risky command to you. See [Security](Security#the-approval-gate).

```yaml
approvals:
  mode: smart
```

### Limit the tools in Telegram

```yaml
toolsets:
  telegram: [web, memory, skills, ask, vision]
```

### Add an MCP server

```yaml
mcp:
  servers:
    github:
      command: [npx, -y, some-mcp-server]
      env:
        GITHUB_TOKEN: ${GITHUB_TOKEN}
```

The tools of this server have the names `mcp_github_<tool>`. The server starts at the first call
and stops when it is idle. See [Tools](Tools#mcp-servers).

### Set a price manually

When the usage report shows no cost for a model, set a price in USD for 1M tokens:

```yaml
pricing:
  overrides:
    <model name>:
      input: 0.5
      output: 1.5
      cache_read: 0.05
```

An override applies to a provider when the name is its `price_key` or its `model`.
