# Tether Agent

Tether Agent is a personal AI agent in one static Go binary. It works from the terminal, from
Telegram, and from cron jobs. It runs shell commands, edits files, reads the web, drives a
browser, keeps a memory, learns skills, and delegates work to subagents.

The agent costs almost nothing when it is idle. Heavy parts start on the first call and stop
when they are idle. These parts are the browser, the MCP servers, and the media programs.

## The main facts

| Fact | Detail |
|---|---|
| **One file** | One static binary (`CGO_ENABLED=0`). There is no runtime to install: no Python, no Node.js. |
| **Fast start** | Startup takes less than 100 ms. A test in the test suite enforces this limit. |
| **Near-zero idle cost** | A feature that you configure but do not use costs almost nothing. It starts on demand. |
| **Any model** | 11 presets for known providers, and any endpoint with the `openai`, `anthropic`, or `responses` wire format. See [Providers](Providers). |
| **Fallback chain** | When a provider entry fails before the first byte, the next entry of the chain takes the call. The order is the entry of the `/model` pin, if the session has a pin, then `primary`, then the entries of `agent.fallback`. See [The fallback chain](Providers#the-fallback-chain). |
| **Reasoning effort** | One scale from `none` to `max`. Set a default for each provider entry, or use `/effort` in a session. |
| **You own the data** | The memory is plain Markdown. All state is in one SQLite file. |
| **Security you can audit** | An approval gate checks dangerous shell commands. A skill guard scans skills before they install. Secrets stay out of the model context and the logs. |

## What it can do

- Chat with you in the terminal (`tether chat`) or in Telegram, with streamed answers and buttons.
- Use a model from xAI, OpenAI, Anthropic, OpenRouter, Z.AI, DeepSeek, Moonshot, Alibaba,
  MiniMax, or Ollama Cloud through a preset. A custom entry connects to any compatible
  endpoint, for example a local model server.
- Run shell commands, read and edit files, search and fetch web pages, and drive headless Chrome.
- Run scheduled jobs. A script job sends its output to Telegram and uses no model tokens.
- Remember facts about you and your work between sessions.
- Write, install, and use skills: folders of instructions that the agent loads when it needs them.
- Send work to parallel subagents, also in the background.
- Use tools from MCP servers.
- Make images, speak answers as voice notes, and transcribe your voice notes.

## Quick start

Download the archive for your platform from the
[releases page](https://github.com/dnwandana/tether-agent/releases). The platforms are
linux/amd64, linux/arm64, and darwin/arm64. Then run these commands:

```sh
tar -xzf tether-agent_<version>_linux_amd64.tar.gz
install -m 0755 tether ~/.local/bin/tether
tether setup     # provider preset, optional fallback, optional Telegram bot
tether doctor    # checks everything and gives the next action
tether chat      # chat in the terminal
```

The full steps are in [Getting Started](Getting-Started).

## Where to go next

| Page | Read it to |
|---|---|
| [Getting Started](Getting-Started) | Install Tether Agent, run the setup wizard, and send your first message. |
| [Providers](Providers) | Choose a preset, set the API keys, build a fallback chain, and set the reasoning effort. |
| [Telegram](Telegram) | Connect a Telegram bot, pair your account, and use the bot in groups. |
| [Commands](Commands) | Find each CLI command and each slash command. |
| [Configuration](Configuration) | Set providers, models, toolsets, and the most important keys. |
| [How It Works](How-It-Works) | Understand a turn, the provider layer, the system prompt, memory, sessions, and compaction. |
| [Tools](Tools) | See the toolsets, the browser, MCP servers, media, and delegation. |
| [Skills](Skills) | Write, install, and manage skills, and learn what the skill guard does. |
| [Cron](Cron) | Schedule jobs and send their results to Telegram. |
| [Goals](Goals) | Keep the agent at work toward a goal, and follow its task list. |
| [Security](Security) | Learn the approval gate, protected paths, secret redaction, and the rules for provider keys. |
| [Running as a Service](Running-as-a-Service) | Run the gateway with systemd, update the binary, and back up your data. |
| [Troubleshooting](Troubleshooting) | Fix common problems with the help of `tether doctor`. |

## License

Tether Agent uses the MIT License. It is inspired by
[Hermes Agent](https://github.com/NousResearch/hermes-agent) from Nous Research. The
[NOTICE](https://github.com/dnwandana/tether-agent/blob/main/NOTICE) file lists the parts that
come from Hermes Agent.
