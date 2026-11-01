# Getting Started

This page shows how to install Tether Agent, set it up, and send the first message. At the
end, you have a working agent in the terminal. Telegram and the systemd service are optional,
and other pages describe them.

## Requirements

- **A supported platform.** Releases exist for these platforms:
  - `linux/amd64`
  - `linux/arm64`
  - `darwin/arm64` (macOS on Apple silicon)

  There is no release for Intel macOS and no release for Windows.
- **An API key for a model provider.** The setup wizard has a preset for each of these
  providers: xAI, OpenRouter, OpenAI, Anthropic, Z.AI, DeepSeek, Moonshot, Alibaba, MiniMax,
  and Ollama. The Ollama preset is Ollama Cloud. For another endpoint, for example a local
  model server, use a custom entry with one of the three wire formats:
  - `openai`: the OpenAI Chat Completions API. Many endpoints are compatible with it.
  - `anthropic`: the Anthropic Messages API.
  - `responses`: the OpenAI Responses API.

  Refer to [Providers](Providers) for the presets and the wire formats.
- **Optional host programs.** Some tools use programs on the host: Chrome or Chromium,
  `python3`, `pdftotext`, `git`, `ffmpeg`, and the programs for text to speech and speech to
  text. Tether Agent works without them, but the related tools are not available. The setup
  wizard shows which programs are missing and how to install them.

The binary is static. You do not need a runtime, a container, or a database server.

## Install from a release archive

1. Open the [releases page](https://github.com/dnwandana/tether-agent/releases) and find the
   latest release.
2. Download the archive for your platform and the `checksums.txt` file. The archive name is
   `tether-agent_<version>_<os>_<arch>.tar.gz`. The version in the name has no `v`.
3. Check the archive:

   ```sh
   sha256sum -c --ignore-missing checksums.txt
   ```

   On macOS, use `shasum -a 256 -c --ignore-missing checksums.txt`.
4. Extract the archive:

   ```sh
   tar -xzf tether-agent_<version>_linux_amd64.tar.gz
   ```

5. Install the binary in a folder on your `PATH`:

   ```sh
   install -m 0755 tether ~/.local/bin/tether
   ```

6. Make sure that the binary runs:

   ```sh
   tether version
   ```

Run `tether` with no command to see all the commands. Run `tether help <command>` to see the
help of one command.

## Build from source

You need Go 1.26 or later.

1. Clone the repository:

   ```sh
   git clone https://github.com/dnwandana/tether-agent.git
   cd tether-agent
   ```

2. Build the binary:

   ```sh
   make build
   ```

   The binary is `bin/tether`. To set a version, use `make build VERSION=v1.2.3`.
3. Copy `bin/tether` to a folder on your `PATH`.

`go install github.com/dnwandana/tether-agent/cmd/tether@latest` also works. A build without
`VERSION=`, from `make build` or from `go install`, has the version `dev`. A `dev` build has
these differences:

- `tether update` does not run unless you add `--force`.
- `tether doctor` does not check for a newer release.

## Run the setup wizard

Run the wizard:

```sh
tether setup
```

The wizard creates the data dir (`~/.tether`, mode 0700). Then it shows these lines:

```text
tether setup. The data folder is /home/you/.tether.
Press Enter to keep the value in [brackets].
```

The wizard has five steps. When you run the wizard again, the current values are the
defaults. Thus you can change one part and keep the rest.

Rules for the answers:

- A `[y/N]` question accepts only `y` or `yes` as yes. Another answer, or Enter, is no.
- A menu question accepts a number from the menu. After a bad answer, the wizard shows
  `Enter a number from 1 to N.` and asks again.
- In a terminal, the wizard hides the API key and the bot token while you type. The question
  then ends with `(the input is hidden):`.

### Step 1: the primary provider

The wizard shows `Step 1 of 5: the primary provider` and the preset menu:

```text
   1) xAI
   2) OpenRouter
   3) OpenAI
   4) Anthropic
   5) Z.AI
   6) DeepSeek
   7) Moonshot
   8) Alibaba
   9) MiniMax
  10) Ollama
  11) Custom
Provider [1]:
```

One item of the menu asks a second question:

| Item | Question | Choices |
|---|---|---|
| Z.AI | `Plan [1]:` | `1) Pay as you go` (preset `zai`), `2) Coding Plan` (preset `zai-coding`) |

On a second run, the default is the current preset. If the entry has no preset, the default
is `11) Custom`.

#### A preset

A preset gives the wire format, the base URL, and the name of the key variable. The wizard
asks only two questions:

1. `API key (<KEY_ENV>)`, for example `API key (OPENROUTER_API_KEY)`.
2. `Model`. Before this question, the wizard shows the model list of the provider. Refer to
   [The model list](#the-model-list).

| Item | Key variable |
|---|---|
| xAI | `XAI_API_KEY` |
| OpenRouter | `OPENROUTER_API_KEY` |
| OpenAI | `OPENAI_API_KEY` |
| Anthropic | `ANTHROPIC_API_KEY` |
| Z.AI (both plans) | `ZAI_API_KEY` |
| DeepSeek | `DEEPSEEK_API_KEY` |
| Moonshot | `MOONSHOT_API_KEY` |
| Alibaba | `DASHSCOPE_API_KEY` |
| MiniMax | `MINIMAX_API_KEY` |
| Ollama (Ollama Cloud) | `OLLAMA_API_KEY` |

If the key variable already has a value in the environment or in `.env`, the question gets
`[Enter keeps the current key]`. In a terminal, the question then ends with
`[Enter keeps the current key] (the input is hidden):`. Press Enter to keep that key.

Some providers need one more step:

- **Ollama.** The preset is Ollama Cloud, so it needs `OLLAMA_API_KEY`. For a local Ollama
  server, use `Custom`.
- **Moonshot.** The K3 models unlock only after a top-up of at least $1.
- **Z.AI.** The standard API and the Coding Plan use different API keys.

Refer to [Providers](Providers#notes-for-some-providers) and
[Local models](Providers#local-models) for the details.

#### Custom

Use `Custom` for an endpoint that has no preset, for example a local model server. The wizard
asks four questions:

1. `Format (openai, anthropic, or responses) [openai]`. This is the wire format.
2. `Base URL`. The default is `https://api.anthropic.com` for `anthropic`. For the other two
   wire formats, the default is `https://api.openai.com/v1`.
3. `API key (or ${NAME} for a variable in .env)`.
4. `Model`. Before this question, the wizard shows the model list of the endpoint. On the
   first run, this question has no default.

#### The API key

The wizard writes a new key to `~/.tether/.env`. It never writes the key to `config.yaml`.

| Entry | Variable in `.env` | Value in `config.yaml` |
|---|---|---|
| A preset | The key variable of the preset, for example `OPENROUTER_API_KEY` | No `api_key` line. The preset reads the key from its variable. |
| Custom, as the primary provider | `PRIMARY_API_KEY` | `api_key: ${PRIMARY_API_KEY}` |

If `config.yaml` has a literal key from an older setup, the wizard moves that key to `.env`.

Two entries can use the same preset with different keys. Then the second key goes to a new
variable with a number, for example `OPENROUTER_API_KEY_2`. The entry gets
`api_key: ${OPENROUTER_API_KEY_2}`.

> **Tip: use a variable that you already have.** At the key question, type `${NAME}`, for
> example `${MY_KEY}`. The wizard then writes `api_key: ${MY_KEY}` and does not store a new
> key. If the variable is not in the environment or in `.env`, the wizard shows
> `Note: MY_KEY is not set in .env or the environment.`

If you press Enter and no key exists, the wizard shows `Note: no API key is set.` The test call
then fails for a provider that needs a key. A custom entry with no key gets no `api_key` line,
so the agent sends no auth header. Use this for a local model server.

#### The model list

The wizard asks for the key before the model. Then it gets the model list from the provider,
with the base URL and the key of the entry. It shows the models as numbered items:

```text
Models of DeepSeek:
   1) deepseek-flash
   2) deepseek-v4-pro
Type a number, a text to filter the list, or a model ID.
Model [deepseek-v4-pro]:
```

- The name after `Models of` is the provider of the preset. A custom entry shows its base URL,
  for example `Models of https://llm.example.com/v1:`.
- The question has a default only on a second run. The default is the current model of the
  entry. When you change the entry from one preset to another preset, the wizard clears the
  model, so the question has no default. A new entry has no default.
- The wizard shows at most 20 models. A longer list ends with a line such as
  `… and 41 more. Type a text to filter the list.`
- The list does not show models that cannot chat, for example embedding, speech, and image
  models. Refer to [Providers](Providers#the-model-list).

For OpenRouter, the list has two groups. It shows only models with tool support:

```text
Models of OpenRouter. Most used this week, with tool support:
   1) deepseek/deepseek-v4.1-flash-1017
   2) z-ai/glm-5.3-flash
   …
  10) deepseek/deepseek-v4-flash-0731
Best for agents:
  11) anthropic/claude-fable-5.1
   …
  15) meta/muse-spark-1.3
Type a number, a text to search, or a model ID.
Model:
```

Type one of these answers at the `Model` question:

| Answer | Result |
|---|---|
| Enter | The default. With no default, the wizard shows `A value is necessary.` and asks again. |
| A number | The model with that number in the last list. A number out of range gets `Enter a number from 1 to 15.` for a list of 15 models. |
| A model ID from the list | That model. For example, `glm-5.3` selects `glm-5.3`, also when `glm-5.3-flash` exists. |
| Another text | A filter. The wizard shows the models that contain the text, without regard to case, as a new numbered list. The numbers then point to the new list. For OpenRouter, the wizard searches the full OpenRouter catalog. |

When no model matches, the wizard asks
`No model matches "glm-6". Use "glm-6" as the model ID? [y/N]`. Type `y` to use the text as the
model ID. Press Enter to answer the model question again.

When the list call fails, the wizard shows the cause and asks for the model ID:

```text
The model list is not available: HTTP 404.
Model:
```

Type the model ID of your provider. The wizard accepts each ID, and the test call checks it. On
a second run, the current model is the default. When the test call fails, the second try gets
the model list again. Refer to
[Troubleshooting](Troubleshooting#the-wizard-or-model-shows-no-model-list).

#### The test call

After the questions, the wizard sends a test call with a limit of one output token. The test
call has a time limit of 10 seconds, so a slow endpoint fails the test. A good call shows this
line:

```text
The test call works: model z-ai/glm-5.3 answered in 812 ms.
```

If the call fails, the wizard shows `The test call failed: <cause>` and
`Check the values and try again.` Then it shows the menu and the questions again. The
current answers are the defaults. If the second try also fails, the setup stops with this
error:

```text
setup: the test call to provider primary failed two times: <cause>. Check the base URL, the API key, and the model, then run tether setup again
```

The wizard masks the key in each error. The wizard saves the entry only after a good test
call. It writes `providers.primary` in `config.yaml`, for example:

```yaml
providers:
  primary:
    preset: openrouter
    model: z-ai/glm-5.3
```

### Step 2: the fallback providers (optional)

The fallback chain has this order:

1. The entry of the `/model` pin, if the session has a pin.
2. The primary provider.
3. The entries of `agent.fallback`, in their order.

The agent goes to the next entry only when an entry fails before the first byte of the
answer. Refer to [The fallback chain](Providers#the-fallback-chain) for the full rules.

The wizard shows the current list, for example `The fallback list is: none.` Then it asks
`Change the fallback list? [y/N]`. Answer no to keep the list.

If you answer yes, the wizard builds a new list. The new list starts empty. Do these steps
for each fallback provider:

1. Answer yes at `Add a fallback provider? [y/N]`.
2. Select an item from the preset menu. This menu is the same as in step 1.
3. Type a name at `Name of the entry`. The default is the preset id, for example
   `[deepseek]`, or `[custom]` for a custom entry.
4. Answer the questions of the item. They are the same as in step 1.

The wizard sends a test call for each entry. It adds the entry to the end of the list after
a good call. Answer no at `Add a fallback provider?` to stop. The wizard then shows the new
list, for example `The fallback list is: deepseek, openrouter.`

The rules for the name of an entry:

- The name `primary` is not permitted. It is the name of the primary provider.
- A name can contain only letters, digits, `-`, and `_`.
- A name can be in the list only one time.
- If a name or a preset is in use already, the default gets a number, for example
  `openrouter-2`.
- If an entry with the name exists, the wizard shows `The entry <name> exists. The answers
  change it.`

A custom fallback entry writes its key to `<NAME>_API_KEY` in `.env`. The wizard makes the
name with capital letters and changes `-` to `_`. For example, the entry `my-proxy` uses
`MY_PROXY_API_KEY`.

The wizard saves the list after each new entry. An old entry that you do not add again stays
in `providers`, but it is not in the list.

> **Note:** an answer of yes at `Change the fallback list?` and then no at the first
> `Add a fallback provider?` writes `agent.fallback: []`. That turns off the fallback chain.

### Step 3: Telegram (optional)

The wizard asks `Set up Telegram? [y/N]`. If Telegram is on already, it asks `Change the
Telegram setup? [y/N]` instead. If you answer yes, do these steps:

1. Stop the gateway if it runs. Telegram lets only one program read the messages of a bot.
2. Get a bot token from [@BotFather](https://t.me/BotFather).
3. Paste the token at `Bot token from @BotFather`. The wizard checks the token with `getMe`
   and shows the bot name, for example `The bot is @my_tether_bot.`
4. Send `/start` to your bot in a private chat. The wizard shows
   `Send /start to @my_tether_bot now. The wait is at most 5m0s.`
5. Answer yes at `Approve <name> (<id>) as the owner? [y/N]`. The wizard shows the pairing
   code of your user, for example `Pairing code: K7Q2MXRP`, before this question.

The wizard writes the token to `.env` as `TELEGRAM_BOT_TOKEN`. The config refers to it as
`${TELEGRAM_BOT_TOKEN}`. After a yes, the wizard adds your user id to
`telegram.allowed_users` and sets `telegram.enabled: true`. It then shows
`<name> (<id>) is the owner. The gateway accepts the messages of this user.`

Other results of this step:

- If the token check fails two times, the setup stops.
- If no `/start` arrives in 5 minutes, the wizard shows
  `No /start received; run tether setup again later.` The setup then continues.
- If you answer no at the owner question, the wizard sets `telegram.enabled: true` but adds
  no owner. It shows the command to approve the user later:
  `tether pairing approve telegram <code>`.

Refer to [Telegram](Telegram) for pairing, groups, and the other settings.

### Step 4: the host programs

This step only shows information. The wizard shows one line for each optional host program.
A missing program has a ⚠ mark, the feature that is lost, and an install command for your
OS. For example, on Ubuntu:

```text
⚠ chrome: not found, so the browser_* tools fail → install Chromium (sudo apt install chromium-browser), or set browser.chrome_path
✓ python3: /usr/bin/python3
```

### Step 5: the systemd service (optional)

On Linux, the wizard asks `Write the systemd unit? [y/N]`. If you answer yes, it writes a
user unit to `~/.config/systemd/user/tether.service`. With `sudo tether setup --system`, it
writes a system unit to `/etc/systemd/system/tether.service`. The wizard prints the commands
that start the service, but it does not run them. Refer to
[Running as a Service](Running-as-a-Service).

On macOS, the wizard shows `systemd is only on Linux, so this step does nothing on darwin.`

At the end, the wizard shows `The setup is complete. Run tether doctor to check everything.`

### The files that the wizard writes

| File | Content |
|---|---|
| `~/.tether/config.yaml` | The settings. The wizard keeps your comments and the keys that it does not know. |
| `~/.tether/.env` | The API keys and the bot token, as `NAME=value` lines. |
| `config.yaml.bak`, `.env.bak` | A copy of the old file, made before the first change of each run. |
| `~/.tether/state.db` | The pairing data of the owner. Only step 3 writes it. |
| `~/.config/systemd/user/tether.service` | The user unit. Only step 5 writes it. |

`config.yaml` and `.env` get mode 0600. The wizard writes each file through a temporary file
and a rename. Thus a crash does not leave a half-written file. The wizard saves the files
after each step that changes them. If the wizard stops in a later step, the earlier steps
stay saved.

> **Caution:** a variable in the process environment wins over the same variable in `.env`.
> A systemd service does not get the variables of your shell. Keep the keys of the service
> in `.env`.

## Send the first message

Start a chat in the terminal:

```sh
tether chat
```

Type a message and press Enter. The answer streams into the terminal. Each tool call shows as
one dim line.

- To stop a running answer, press **Ctrl-C**. The session stays, and you can type the next
  message. Ctrl-C also pauses an active goal.
- To quit, type `/exit`, press **Ctrl-D**, or press **Ctrl-C** at the prompt.
- To see the slash commands, type `/help`. Refer to [Commands](Commands).

The chat keeps the input history in `~/.tether/.history`. The next `tether chat` continues the
same session. Type `/new` to start a new session.

## Check the installation

Run the checks:

```sh
tether doctor
```

Each line shows ✓, ⚠, or ✗, and a next action when there is a problem. A ✗ is a failure, and
the command then exits with code 1. A ⚠ is a warning, for example a missing optional program.
Refer to [Troubleshooting](Troubleshooting) for the full list of checks.

The doctor shows one line for each provider entry: first `primary`, then the fallback chain,
then the other entries. A good line looks like this:

```text
✓ provider primary (openrouter): model z-ai/glm-5.3 answered in 812 ms
```

> **Note:** the doctor sends one test call to each provider entry. Each call has a limit of
> one output token, but the provider can charge for the input tokens too.

## The data dir

Tether Agent keeps all its state in one folder. It finds the folder in this order:

1. The `--data-dir DIR` flag.
2. The `TETHER_HOME` environment variable.
3. `~/.tether`.

Use the same data dir for every command. A command with a different data dir reads and
changes a different `state.db`.

| Path | Content |
|---|---|
| `config.yaml` | The settings. Refer to [Configuration](Configuration). |
| `.env` | The secrets. The mode must be 0600. |
| `state.db` | The SQLite database: sessions, messages, usage, cron jobs, pairing, and approvals. |
| `gateway.lock` | The lock and the process id of the running gateway. |
| `logs/tether.log` | The log of `chat` and `gateway`. |
| `memory/memory.md` | The notes that the agent keeps. |
| `memory/user_profile.md` | The facts about you that the agent keeps. |
| `skills/` | The skills. Refer to [Skills](Skills). |
| `scripts/` | The scripts for cron jobs. Refer to [Cron](Cron). |
| `cache/` | Price data, long tool outputs, media, screenshots, the browser profile, and the MCP tool lists. |
| `exports/` | The output of `tether sessions export`. |
| `backups/` | The output of `tether backup`. |
| `.history` | The input history of `tether chat`. |

The agent cannot write `.env`, `config.yaml`, `state.db`, `skills/`, `scripts/`, or
`backups/` with its file tools. Refer to [Security](Security).

## Next steps

- [Providers](Providers): add fallback providers, change the model, and set the reasoning
  effort.
- [Telegram](Telegram): talk to the agent from your phone.
- [Configuration](Configuration): change the model, the tools, and the limits.
- [Running as a Service](Running-as-a-Service): keep the gateway on all the time.
- [Cron](Cron): give the agent tasks on a schedule.
