# Tools

The agent does its work through tools. Each tool belongs to one toolset. This page tells what each
toolset does, what it needs on the host, and how to configure it.

The [tool reference](https://github.com/dnwandana/tether-agent/blob/main/docs/TOOLS.md) gives the
full parameters of each tool. The
[configuration reference](https://github.com/dnwandana/tether-agent/blob/main/docs/CONFIG.md)
gives the default of each key on this page.

## The toolsets

| Toolset | Purpose | Tools | Requirement |
|---|---|---|---|
| `terminal` | Run shell commands on the host, in the foreground or in the background. | `terminal` | None. Each command goes through the [approval gate](Security#the-approval-gate). |
| `file` | Read, write, and edit files. | `read_file`, `write_file`, `patch` | None. The tools refuse [protected paths](Security#protected-paths). |
| `web` | Search the web and read pages as Markdown. | `web_search`, `web_fetch` | None. |
| `browser` | Control a headless Chrome. | `browser_navigate`, `browser_click`, `browser_fill`, `browser_read`, `browser_run_js`, `browser_take_screenshot`, `browser_cookies` | Chrome or Chromium on the host. |
| `vision` | Look at an image. | `view_image` | `agent.vision_provider`, or `vision: true` on the primary provider. |
| `ask` | Ask the user a question. | `clarify` | None. |
| `memory` | Keep facts and memory notes, and search past sessions. | `memory`, `session_search` | None. |
| `skills` | Use and write skills. | `skills_list`, `skill_view`, `skill_manage` | None. |
| `cron` | Create and manage scheduled jobs. | `cronjob` | The gateway runs the jobs. |
| `delegation` | Start child agents. | `delegate_task` | The gateway, for background batches. |
| `code_execution` | Run a Python script that calls tools. | `execute_code` | `python3` in `PATH`. |
| `media` | Make images, speech, and transcripts. | `generate_image`, `text_to_speech`, `transcribe_audio` | A backend in `media.*`. |
| `todo` | Keep a task list for work with many steps. | `todo_list` | None. |
| `mcp` | The tools of all MCP servers. | `mcp_<server>_<tool>` | The servers in `mcp.servers`. |

Each MCP server also has its own toolset, `mcp_<server>`.

Some tools are not registered when their requirement is missing:

- `view_image` needs `agent.vision_provider`, or `vision: true` on the primary provider.
- `execute_code` needs `python3` in `PATH`.
- A media tool needs a backend. `generate_image` has no default backend.

A browser tool stays registered without Chrome. A call then returns an error.

## Turn toolsets on or off

`toolsets.<channel>` sets the toolsets of each channel. The channels are `cli`, `telegram`, and
`cron`. The default of each channel is `[all]`.

```yaml
toolsets:
  cli: [all]
  telegram: [web, file, memory, skills, ask]
  cron: [web, file, memory]
```

- The agent does not see a hidden tool. The program removes the tool before it sends the request.
- A call to a hidden tool returns an error.
- A cron job or a `delegate_task` call can give its own `toolsets` list. That list can only make the
  set of the channel smaller. It cannot add a toolset.
- There is no slash command that changes the toolsets. Edit `config.yaml`, then restart.

## Web

`web_search` searches with DuckDuckGo. It returns a numbered list of titles, URLs, and snippets.
The default is 5 results, and the maximum is 10.

`web_fetch` downloads pages with a plain HTTP GET. It returns the main text of each page as
Markdown.

- One call takes a maximum of 5 URLs (`web.fetch_max_urls`).
- No JavaScript runs. Use the browser tools for a page that needs JavaScript.
- PDF files are not supported. The agent can download a PDF with `curl` and convert it with
  `pdftotext`.

When `web.block_private_networks` is `true` (the default), the web tools refuse loopback, private,
link-local, and CGNAT addresses. The check runs on the IP address after DNS resolution.

## Vision

`view_image` sends one image and a question to a vision model. It returns the answer text.

- The image is a local file or an `http` or `https` URL. The maximum size is 10 MB.
- A URL download follows `web.block_private_networks`.
- The local path can come from an `[image: <path>]` marker or from a browser screenshot.

The tool chooses its provider in this order:

1. `agent.vision_provider`, when it is set. The call goes to that provider entry only, with no
   fallback chain. The value `primary` uses the default fallback chain.
2. The primary provider, when its entry has `vision: true`. The call goes through the default
   fallback chain, but only the entries with `vision: true` get it.
3. No provider. The tool is not registered.

The same vision provider describes the static stickers that you send in Telegram. An entry with
`vision: true` also gets the images of the conversation inline. Refer to
[Providers](Providers).

## Browser

The browser tools control one headless Chrome through the Chrome DevTools Protocol. Each session
has its own tab.

### Install Chrome

Tether Agent finds Chrome in this order:

1. `browser.chrome_path` in `config.yaml`.
2. The `CHROME_PATH` environment variable.
3. `google-chrome`, `chromium`, or `chromium-browser` in `PATH`.
4. `/snap/bin/chromium` on Linux, or the Chrome and Chromium apps on macOS.

Install Chrome with the command for your system:

| System | Command |
|---|---|
| Ubuntu | `sudo apt install chromium-browser` |
| Debian | `sudo apt install chromium` |
| macOS | `brew install --cask google-chrome` |

### How the browser runs

- Chrome starts at the first browser call.
- Chrome stops after `browser.idle_close_minutes` minutes with no browser call. The default is 10.
- All sessions use one Chrome profile in `~/.tether/cache/chrome-profile/`. The sessions share the
  cookies and the logins. The profile stays after a restart.
- `browser_cookies` with `get` returns no cookie values, only the names, the domains, the expiry,
  and the length of each value.
- The browser tools do not apply `web.block_private_networks`. A page can open a local address.

```yaml
browser:
  chrome_path: ""          # "" finds Chrome automatically
  idle_close_minutes: 10
```

## MCP servers

Tether Agent is an MCP client. It supports the stdio, Streamable HTTP, and SSE transports. It does
not support OAuth.

### Add a server

Add an entry under `mcp.servers`. Use `command` for a stdio server, or `url` for an HTTP server.

```yaml
mcp:
  servers:
    github:
      command: [npx, -y, some-mcp-server]
      env:
        GITHUB_TOKEN: "${GITHUB_TOKEN}"
    remote:
      url: https://example.com/mcp
      headers:
        Authorization: "Bearer ${REMOTE_TOKEN}"
```

- `transport` is `""` for Streamable HTTP, or `sse`.
- `env` gives variables to a stdio server. A stdio server gets no other secrets.
- `enabled: false` turns off a server and keeps its entry.
- The [configuration reference](https://github.com/dnwandana/tether-agent/blob/main/docs/CONFIG.md)
  gives the timeouts and the other keys.

### Tool names

An MCP tool has the name `mcp_<server>_<tool>`. The program changes characters that a provider does
not accept to `_`.

Two server names must stay different after this change, also when the case is ignored. For example,
`a b` and `a_b`, or `GitHub` and `github`, are the same name. The program refuses such a
configuration, because the two servers share the tool names and the cache file.

### How a server runs

An idle server costs nothing:

1. At startup, the program reads the tool list of each server from a cache in
   `~/.tether/cache/mcp-tools/`. No server process starts.
2. A server with no cache starts one time to list its tools. Then it stops.
3. The first call to a tool starts its server.
4. The server stops after `idle_stop_minutes` minutes with no call. The default is 10.

Set `always_on: true` to keep a server connected all the time.

### Reload the servers

Send `/reload-mcp` to read the server list again. In the Telegram menu, the command is
`/reload_mcp`. The program restarts the servers and lists their tools again. You do not have to
restart the gateway. The changes apply from the next message.

> [!WARNING]
> Enable only MCP servers that you trust. The approval gate does not check MCP tools, because
> Tether Agent cannot see what a server runs.

## Media

The media tools make images, speech, and transcripts. Each tool has a backend:

- `command`: a command on the host. The program runs it directly, not through a shell.
- `endpoint`: a provider entry. The tool sends the OpenAI media requests to the `base_url` of
  the entry: `/audio/speech`, `/audio/transcriptions`, or `/images/generations`. The `format` of
  the entry has no effect, and the call has no fallback chain.
- `""`: no backend. The tool is hidden.

A command template can use these placeholders: `{text}`, `{in}`, `{out}`, `{out_base}`,
`{prompt}`, and `{size}`. A media command stops after 120 seconds.

### The defaults

| Tool | Default backend | Install |
|---|---|---|
| `text_to_speech` | `edge-tts --voice en-US-AriaNeural --text {text} --write-media {out}` | `pipx install edge-tts` |
| `transcribe_audio` | `whisper-cli -m ~/models/ggml-base.bin -f {in} -otxt -of {out_base}` | `brew install whisper-cpp` on macOS. Build whisper.cpp yourself on Linux. |
| `generate_image` | None | Set an `endpoint` backend. |

To use a provider endpoint, add a provider entry and point the media tool at it. An empty
`provider` uses `primary`. The entry must exist under `providers:`. If it does not exist, the
tool is hidden and the log shows a warning. A preset entry also works, because the preset fills
the `base_url` and the key.

```yaml
providers:
  primary:
    # ... your primary entry ...
  openai_audio:
    preset: openai          # reads OPENAI_API_KEY from .env
    model: <speech model>
  openai_img:
    preset: openai
    model: <image model>
media:
  tts:
    backend: endpoint
    provider: openai_audio
  image:
    backend: endpoint
    provider: openai_img
```

### ffmpeg

ffmpeg is optional. It does two jobs:

- It changes audio to 16 kHz WAV before the speech-to-text command.
- It changes speech output to OGG/Opus, so Telegram can show it as a voice note. Without ffmpeg,
  Telegram gets the audio as a file.

### Voice notes in Telegram

When speech-to-text is set up, the program transcribes each voice note before the agent reads it.
Without speech-to-text, the agent gets the text
`[voice message received, but transcription is not set up]`.

## Delegation

`delegate_task` starts child agents. Each child runs in a new session with a short, focused
prompt. The parent sees only the summary of each child.

There are two modes:

- One `goal` starts one child.
- A `tasks` list starts a batch of children that run in parallel.

### The limits

| Key | Default | Effect |
|---|---|---|
| `delegation.max_concurrent_children` | `5` | The maximum number of children at the same time. |
| `delegation.max_iterations` | `50` | The maximum number of tool rounds in one child. |
| `delegation.child_timeout_s` | `600` | The timeout of one child, in seconds. |
| `delegation.max_spawn_depth` | `1` | The depth of child agents. With `1`, a child cannot start a child. |

`agent.delegation_provider` can give the children a different provider entry, for example a
less expensive model. The child chain starts at that entry, then `primary` and the entries of
`agent.fallback`. When the key is `""`, the children use the chain of the parent session. A
child also copies the reasoning effort of the parent session. Refer to
[How It Works](How-It-Works#child-runs).

### What a child cannot do

A child cannot call these tools:

- `delegate_task` (an orchestrator child can, when the depth limit permits it)
- `clarify`
- `memory`
- `execute_code`
- `cronjob`

A child can only make its toolsets smaller than the toolsets of its parent.

### Background batches

In the gateway, `background: true` returns a batch id at once. The parent can continue its work.
When the batch ends, the results come back as a new turn with the text
`[Background delegation <id> finished]`. `/stop` cancels the children.

## Code execution

`execute_code` runs a Python script on the host. It needs `python3` in `PATH`.

The script can call some tools as Python functions:

```python
from tether_tools import web_fetch, ToolError
```

A script can call only these tools: `web_search`, `web_fetch`, `read_file`, `write_file`,
`patch`, and `terminal`. Each call goes through the same checks as a call from the model.

| Key | Default | Effect |
|---|---|---|
| `code_execution.timeout_s` | `300` | The timeout of one script, in seconds. |
| `code_execution.max_tool_calls` | `50` | The maximum number of tool calls from one script. |
| `code_execution.max_stdout_bytes` | `50000` | The maximum output that goes back to the model. |

- The script runs in a scratch folder.
- The script gets an environment with no secrets. The exceptions are the names in
  `terminal.env_passthrough` and the `required_environment_variables` of a loaded skill.
- The approval gate checks the full text of the script before it runs. Refer to
  [Security](Security#the-approval-gate).
