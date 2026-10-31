# Troubleshooting

Start with `tether doctor`. It checks the setup and gives a next action for each problem. If
the doctor does not find the cause, look at the problem list and the logs on this page.

## What `tether doctor` checks

```sh
tether doctor
```

Each line shows one of three marks:

| Mark | Meaning |
|---|---|
| ✓ | The check passed. |
| ⚠ | A warning. Tether Agent works, but a feature is lost or a change is recommended. |
| ✗ | A failure. Tether Agent cannot work correctly until you fix it. The command exits with code 1. |

The doctor runs the checks in this order:

1. **config**: `config.yaml` loads with the values of `.env`. The check fails for each of these
   problems:
   - `providers.primary` is missing, or it is not usable, for example with no API key.
   - A preset is not known, or it is removed.
   - An enum key has an unknown value, for example `format` or `effort`.
   - The `agent.fallback` list is not valid.

   A second **config** line gives a warning for each provider entry with the removed key
   `flat_rate`.
2. **.env**: the file has mode 0600, and each line is correct. A missing file is not a
   problem. A file that other users can read is a failure.
3. **provider** (one line for each provider entry): a test call with a limit of one output
   token. The order is `primary`, then the fallback chain, then the other entries by name. The
   line names the preset, for example `provider primary (openrouter)`. An entry with no API
   key or no model fails with no test call.
4. **telegram**: the bot token works. A warning tells you when the BotFather privacy mode
   blocks group messages that your config needs.
5. **state.db**: the database opens, and the migrations are current.
6. **last answer**: the provider entry and the model of the newest answer. A warning tells
   you that an entry other than the first entry of the turn gave that answer.
7. **host programs**: Chrome, `python3`, `pdftotext`, `git`, `ffmpeg`, and the programs of the
   text to speech and speech to text commands. A missing program is a warning, and the line
   names the feature that is lost. A missing `ffmpeg` is OK when no feature needs it, for
   example when Telegram voice is off.
8. **systemd unit**: a warning when the unit file is from an older release. The doctor
   shows this line only when a unit file exists.
9. **mcp** (one line for each server): the server starts and lists its tools.
10. **disk**: a warning when less than 1 GiB is free.
11. **release**: a warning when a newer release exists or when the check cannot go online.

When the config does not load, the doctor skips the provider, telegram, and mcp checks.

The doctor does not need the gateway lock, so you can run it while the gateway runs. It masks
the secrets in its output.

## Problems and fixes

### `config: providers.primary is missing`

**Cause:** `config.yaml` does not exist, or it has no `providers.primary` entry.
`tether chat` and `tether gateway` need a primary provider.

**Fix:** run `tether setup`.

### `.env has mode 0644; run chmod 600 ~/.tether/.env`

**Cause:** the group or other users have a permission on `.env`. The file holds your secrets,
so Tether Agent refuses to read it.

**Fix:** set the mode:

```sh
chmod 600 ~/.tether/.env
```

### `setup: another process gets the updates of this bot`

**Cause:** the gateway runs and reads the messages of the bot. Telegram lets only one program
read the messages of a bot, so it answers the wizard with error 409. The Telegram step of
`tether setup` then stops.

**Fix:**

1. Stop the gateway: `systemctl --user stop tether`.
2. Run `tether setup` again.
3. Start the gateway: `systemctl --user start tether`.

### A new Telegram user gets no answer

**Cause:** the setup wizard adds your user id to `telegram.allowed_users`. When an allow list
exists, the default action for an unknown user in a private chat is `ignore`, not `pair`.

**Fix:** use one of these methods:

- Add the user id to `telegram.allowed_users` in `config.yaml`, then restart the gateway.
- Set `telegram.unauthorized_dm_behavior: pair` and restart the gateway. An unknown user then
  gets a pairing code. Approve the code with `tether pairing approve <code>`.

Refer to [Telegram](Telegram#pairing).

### A command shows old data, or `state.db` does not exist

**Cause:** the command uses a different data dir. This happens when `TETHER_HOME` is set in
one terminal and not in another, or when the gateway runs with `--data-dir`.

**Fix:** use the same data dir for every command. The systemd unit shows the data dir of the
gateway in its `ExecStart=` line.

### The doctor warns that the systemd unit is old

**Cause:** the unit file is from a release before v0.7.0. It does not have the current stop
and restart settings.

The doctor line names each old setting, for example
`The unit has no KillMode=mixed, so systemd stops the running tool commands at the start of the drain.`

**Fix:**

1. Run `tether setup`.
2. Answer yes at `Write the systemd unit?`.
3. Run `systemctl --user daemon-reload`.
4. Run `systemctl --user restart tether`.

For a system unit, use `sudo tether setup --system`, `sudo systemctl daemon-reload`, and
`sudo systemctl restart tether`.

### The browser tools fail with `no Chrome or Chromium found`

**Cause:** Tether Agent cannot find Chrome or Chromium.

**Fix:** install it. For example:

- Ubuntu: `sudo apt install chromium-browser`
- Debian: `sudo apt install chromium`
- macOS: `brew install --cask google-chrome`

If Chrome is in an unusual place, set `browser.chrome_path` in `config.yaml` or the
`CHROME_PATH` environment variable.

### Voice messages are not transcribed

**Cause:** speech to text is not set up, or its program is missing. The agent then gets the
note `[voice message received, but transcription is not set up]`.

**Fix:**

1. Set `media.stt` in `config.yaml`. Refer to [Tools](Tools).
2. Install the program of the command, for example `whisper-cli`, and its model file.
3. Install `ffmpeg`. Tether Agent uses it to convert the audio before the transcription.
4. Run `tether doctor` to check the programs.

### `web_search` fails with `DuckDuckGo blocked the search`

**Cause:** DuckDuckGo sometimes answers with a captcha page instead of results. This happens
more often from a server address.

**Fix:** the agent can search with the browser tools instead, and the error tells it so.
Install Chrome or Chromium so that the browser tools are available.

### A new skill does not show

**Cause:** Tether Agent reads the skills when a session starts.

**Fix:** send `/new` to start a new session.

### The agent does not use a new memory entry

**Cause:** the system prompt of a session does not change after it is made. This keeps the
provider prompt cache valid. A memory change shows in the prompt only after the prompt is made
again.

**Fix:** send `/new`, or wait for the next compaction of the session.

## Provider problems

Refer to [Providers](Providers) for the presets, the wire formats, the fallback chain, and the
reasoning effort.

### `setup: the test call to provider <name> failed two times`

**Cause:** the wizard sends a test call for each provider entry. It asks the questions one
more time after a failure. A second failure stops the setup. The error gives the cause of the
last failure.

**Fix:** find the cause in the sections below. Then run `tether setup` again. The wizard saves
an entry only after a good test call, so the old values of the entry stay.

### The doctor passes, but the service has no key

**Cause:** the key is in a variable of your shell, not in `.env`. The doctor in your shell
finds the key. The systemd service does not get the variables of your shell.

**Fix:** put the key in `~/.tether/.env`, for example `OPENROUTER_API_KEY=...`. Then restart
the service.

### The test call fails with HTTP 401 or HTTP 403

**Cause:** the API key is wrong, or the key has no access to the model. The doctor line shows
`the test call to provider <name> failed: provider <name>: provider failed before the first byte: HTTP 401: <body>`.

**Fix:**

1. Get a new key from your provider.
2. Run `tether setup` and type the key at the key question of the entry.
3. Run `tether doctor`.

> **Caution:** a variable in the process environment wins over the same variable in `.env`.
> If a shell variable holds an old key, `unset` it or correct it.

An HTTP 401, 402, 403, 408, 429, or 5xx starts the fallback chain. Thus the chat can still
answer through a fallback provider while the key of the primary provider is bad.

### `config: providers.<name> needs an API key`

The full text is
`config: providers.<name> needs an API key. Set <KEY_ENV> in ~/.tether/.env, or set providers.<name>.api_key`.

**Cause:** the entry uses a preset, but the key variable of the preset has no value.

**Fix:** add the key to `~/.tether/.env`, for example `OPENROUTER_API_KEY=...`. Or run
`tether setup` again.

The result depends on the entry:

- For `primary`, the config does not load. `tether chat` and `tether gateway` do not start.
- For another entry, the config loads. The gateway log has the warning
  `provider entry is not usable`. The doctor line of the entry is a failure. The fallback
  chain skips the entry and goes to the next entry.

A second form is
`needs an API key, because its base_url is not the URL of the preset`. The entry sets a
`base_url` that is not the URL of its preset. A preset never sends its key to another host.
Set `providers.<name>.api_key`, or remove `base_url`.

### `config: providers.<name>.model is empty`

**Cause:** the entry has no `model`. A preset does not give a model.

**Fix:** set `providers.<name>.model` in `config.yaml`, or run `tether setup`.

The result depends on the entry:

- For `primary`, the config does not load. `tether chat` and `tether gateway` do not start.
- For another entry, the config loads, but the entry is not usable. The log has the warning
  `provider entry is not usable`. The doctor line of the entry is a failure. The fallback chain
  skips the entry and goes to the next entry.

### `config: providers.<name>.preset "<id>" is not known`

**Cause:** the preset id has a spelling error, or it is not in the preset table. The error
lists the known ids.

**Fix:** use an id from the list. Refer to [Providers](Providers#presets) for the table.

### `config: providers.<name>.preset "ollama" is removed`

The full text is
`config: providers.<name>.preset "ollama" is removed. For a local server, remove the preset line and set format: openai and base_url: http://localhost:11434/v1. For Ollama Cloud, use preset: ollama-cloud`.

**Cause:** the entry uses the preset `ollama`, which is not in the preset table any more. The
config does not load.

**Fix:** change the entry in `config.yaml`:

- For a local Ollama server, remove the `preset` line. Set `format: openai` and
  `base_url: http://localhost:11434/v1`.
- For Ollama Cloud, set `preset: ollama-cloud`.

Refer to [Local models](Providers#local-models) for a local server.

### `config: ${NAME} is not set (key providers.<name>.api_key)`

**Cause:** `config.yaml` refers to a variable that is not in `.env` or in the environment.

**Fix:** add `NAME=value` to `~/.tether/.env`.

### The test call fails with HTTP 404 or HTTP 400

**Cause:** the wire format, the base URL, or the model is wrong. Each wire format adds its own
path to the base URL:

| Wire format | Request URL |
|---|---|
| `openai` | `<base_url>/chat/completions` |
| `responses` | `<base_url>/responses` |
| `anthropic` | `<base_url>/v1/messages`, or `<base_url>/messages` when the base URL ends with `/v1` |

**Fix:**

1. Read the API docs of your provider. Find the wire format and the base URL.
2. Use a preset if one exists for your provider. A preset sets the wire format and the base
   URL for you.
3. For a custom entry, correct `format`, `base_url`, and `model` in `config.yaml`.
4. Run `tether doctor`.

An HTTP 404 or a usual HTTP 400 does not start the fallback chain. The same request fails on
each provider, so the error goes to the chat at once. An HTTP 400 about a credit balance or a
quota is different: it starts the fallback chain.

### The wizard or `/model` shows no model list

The wizard shows `The model list is not available: <cause>.` `/model <provider>` shows
`⚠️ No model list from <provider>` and the cause.

**Cause:** the list call to the provider failed. The cause line tells you why:

- `HTTP 401` or `HTTP 403`: the key is wrong or missing.
- `HTTP 404`: the provider has no model list, or the base URL is wrong.
- `HTTP 5xx`, for example `HTTP 503`: the server of the provider failed.
- `no connection`: the host cannot be reached, for example because of a wrong base URL or a
  network error.
- `no chat models`: the list has only models that cannot chat.
- `error`: the provider did not answer in 10 seconds, or the response is not a model list.

**Fix:**

1. In the wizard, type the model ID. The wizard accepts each ID, and the test call checks it.
2. In a chat, send `/model <provider>:<model>`.
3. When the entry has a model in `config.yaml`, use that model. In Telegram, tap
   `Use <model>`. In the CLI, send `/model <provider> config`.
4. Send `/model <provider>` again later. A failed list call is not kept, so each try calls the
   provider again.

For an HTTP 401 or an HTTP 404, also read the test call sections above.

### `config: providers.<name>.format is "<value>", which is not a known value`

**Cause:** the wire format is not `openai`, `anthropic`, or `responses`.

**Fix:** correct `providers.<name>.format`. An entry with a preset needs no `format`.

### `config: agent.fallback ...`

These errors stop the load of the config:

| Error | Fix |
|---|---|
| `config: agent.fallback must not contain primary` | Remove `primary` from the list. `primary` is always in the chain. It is the first entry, unless the session pins an entry with `/model`. |
| `config: agent.fallback names "<name>" two times` | Remove the second copy of the name. |
| `config: agent.fallback names "<name>", which is not in providers` | Add the entry under `providers`, or remove the name from the list. |

### The answers come from a fallback provider

**Cause:** an entry of the fallback chain failed before the first byte of an answer. The chain
then skips that entry for 60 seconds. After that time, the next call tries the entry again.
Possible reasons are a network error, a bad key, an HTTP 429, or an HTTP 5xx from the entry.

The signs are these:

- The doctor shows a ⚠ on the **last answer** line, with this next action:
  `the primary failed at that time; see "provider fallback" in the gateway log`.
- The reply to `/new` shows `Model: <model> (fallback)`.
- The reply to `/new` or `/model` shows a warning line. The line shows only in the 60 seconds
  after the failure, so the time is always `less than 1 min ago`.

The warning line has one of two forms. `<entry>` is the entry that answers now:

- For a chain that starts at `primary`:
  `⚠️ The primary model <model> failed less than 1 min ago (HTTP 503). Calls go to <entry> until the primary answers again.`
- For a chain that starts at an entry that the session pins with `/model`:
  `⚠️ The model <model> of <pinned> failed less than 1 min ago (HTTP 503). Calls go to <entry> until <pinned> answers again.`

**Fix:**

1. Look for `provider fallback` in the log. `tether gateway` and `tether chat` write the same
   line. The line has the fields `from`, `to`, `from_model`, `to_model`, and `err`.
2. Correct the cause, for example the key or the network.
3. Run `tether doctor` to test each entry.

If the session pins an entry with `/model`, the chain starts at that entry. The doctor then
does not warn when that entry answers.

When all entries fail, the answer ends with `error:` and the error of each entry. The fallback
state lives in memory. A restart of the gateway clears it.

### `Unknown effort` after `/effort`

**Cause:** the level is not known. The reply lists the permitted levels:
`default`, `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, and `max`.

**Fix:** send `/effort` with one of these levels. `/effort default` removes the reasoning
effort of the session.

### `config: providers.<name>.effort is "<value>", which is not a known value`

**Cause:** `providers.<name>.effort` in `config.yaml` has a value that is not a level.

**Fix:** use `""`, `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, or `max`. `default` is
only an argument of `/effort`. It is not a config value.

### The model rejects the reasoning effort

**Cause:** some models do not accept a reasoning effort. The provider then answers with an
HTTP 400 that names `effort`, `reasoning`, or `thinking`.

Tether Agent sends the call again with no reasoning effort. If that call works, it does these
steps:

1. It writes a warning to the log, for example
   `provider primary: model <model> rejects the effort "high". The provider default applies`.
2. It sends no reasoning effort to that entry and model for the rest of the gateway run.
3. The reply to `/effort` shows `⚠️ <model> rejects the effort "high". The provider default applies.`

**Fix:** no fix is necessary, because the provider default applies. To remove the warning,
send `/effort default`, or remove `providers.<name>.effort`. A restart of the gateway clears
the record of the rejection.

### `/effort none` does not turn off the thinking

**Cause:** some models always think. On the presets `zai`, `zai-coding`, `moonshot`, `xai`, and
`anthropic`, `none` gives the lowest level of the provider. The `minimax` preset does the same
with `MiniMax-M3.1-Flash`. The agent does not send
`thinking: {"type":"disabled"}`, because these models reject it.

**Fix:** no fix is possible on these presets. To get answers with no thinking, use a model that
can turn it off, for example on the `deepseek` or `alibaba` preset, or `MiniMax-M3` on the
`minimax` preset. Refer to
[Providers](Providers#effort-none-and-the-thinking).

### A local Ollama model forgets the start of the conversation

**Cause:** Ollama cuts the context to its context length. On a GPU with less than 24 GiB of VRAM,
the default is 4K tokens. The system prompt and the tools of the agent need more than that.

**Fix:**

1. Start the Ollama server with `OLLAMA_CONTEXT_LENGTH=64000` or a larger value.
2. Set `providers.<name>.context_window` to the same value.
3. Run `tether doctor`.

The `openai` wire format cannot send `num_ctx`, so only the server setting has an effect. Refer to
[Local models](Providers#local-models).

### A Moonshot K3 model fails, but the key is correct

**Cause:** the K3 models, for example `kimi-k3.1-pro`, unlock only after a top-up of at least $1.

**Fix:** add at least $1 to your Moonshot account. Then run `tether doctor`.

## Get debug logs

Run the command with the log on stderr and the debug level:

```sh
tether chat --log-stderr --log-level debug
tether gateway --log-stderr --log-level debug
```

Without `--log-stderr`, the log goes to `logs/tether.log` in the data dir. The systemd unit
sends the log to the journal:

```sh
journalctl --user -u tether -f
```

The log masks secrets, but read it before you share it.

## Report a problem

Open an issue on [GitHub](https://github.com/dnwandana/tether-agent/issues). Include these
items:

1. The output of `tether version`.
2. The output of `tether doctor`.
3. The steps that cause the problem.
4. The related lines of the log.

Remove all secrets, tokens, and private messages before you send the issue.
