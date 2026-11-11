# Tether Agent

Tether Agent is a personal AI agent in one static Go binary. It works from the CLI, from
Telegram, and from cron jobs. It has tools for the shell, files, the web, a browser, memory,
skills, a task list, delegation, MCP servers, and media. It can give coding tasks to Claude
Code and Codex over the Agent Client Protocol (ACP). With `/goal`, the agent keeps
working over several turns until a judge model says that the goal is done.

The agent uses little CPU and RAM when it is idle. Heavy parts start on the first call and
stop when they are idle. These parts are the browser, the MCP servers, the media programs, and
the adapters of the coding agents.

The [wiki](https://github.com/dnwandana/tether-agent/wiki) has the full guide.

## Quick start

1. Download the archive for your platform from the releases page. The platforms are
   linux/amd64, linux/arm64, and darwin/arm64.

   ```sh
   tar -xzf tether-agent_<version>_linux_amd64.tar.gz
   install -m 0755 tether ~/.local/bin/tether
   ```

   Run `tether` with no command to see all the commands.

2. Run the setup wizard. The wizard asks for your provider from a list of presets. The last
   item, Custom, is for an endpoint with no preset. Then the wizard gets the model list from
   the provider. Type a number, a text to filter the list, or a model ID. If the list call
   fails, type the model ID.

   Then the wizard asks for an optional list of fallback providers and an optional Telegram
   bot. It sends a test call to each provider. It writes `~/.tether/config.yaml` and
   `~/.tether/.env`.

   ```sh
   tether setup
   ```

3. Run the checks. Each line shows ✓, ⚠, or ✗, and a next action. It also tells you when a
   newer release is available.

   ```sh
   tether doctor
   ```

4. On Linux, start the gateway as a systemd user service. The gateway runs Telegram and cron.
   The setup wizard can write the unit file for you. `loginctl enable-linger` keeps the service
   on after you log out.

   ```sh
   systemctl --user daemon-reload
   systemctl --user enable --now tether
   loginctl enable-linger $USER
   ```

5. Chat from the terminal. A `chat` process can run next to the gateway.

   ```sh
   tether chat
   ```

6. Later, get the newest release with `tether update`. Run `tether update --check` to only see
   if there is one. On a terminal, the command asks to restart the gateway. After the update to
   v0.7.0, run `tether setup` once and answer yes at "Write the systemd unit?", so the gateway
   gets the new stop settings.

   ```sh
   tether update
   ```

## Configuration example

This example uses OpenAI as the primary provider. DeepSeek and OpenRouter are the fallback
providers, in that order. Each entry uses a preset, so it needs only a model. The preset gives
the format, the base URL, the name of the key variable, and the provider quirks. Put the API
keys in `~/.tether/.env` (mode 0600).

```yaml
# ~/.tether/config.yaml
providers:
  primary:
    preset: openai
    model: gpt-6.1-sol
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
OPENAI_API_KEY=...
DEEPSEEK_API_KEY=...
OPENROUTER_API_KEY=...
```

Each model call goes through a chain of entries. The order is the entry of the `/model` pin,
then `primary`, then the entries of `agent.fallback`. The call goes to the next entry only on
one of these failures before the first byte of the answer:

- No connection.
- HTTP 5xx, 429, 401, 402, 403, or 408.
- An HTTP 400 with a billing or quota text.

Other errors stop the call. The chain skips an entry that is not usable, for example an entry
with no API key. The chain skips a failed entry for 60 seconds. A turn stays on the
entry that answered. `agent.fallback: []` turns off the fallback. If the config has no
`agent.fallback` key, an entry with the name `fallback` is the list. See
[The fallback chain](https://github.com/dnwandana/tether-agent/wiki/Providers#the-fallback-chain) in the wiki.

Send `/model` to change the model of a session. It shows the providers. `/model <provider>`
shows the model list of that entry: buttons in Telegram, and a numbered list in the CLI. Send
`/effort` to change the reasoning effort. All keys and their default values are in
[docs/CONFIG.md](docs/CONFIG.md).

## Backup and restore

`tether backup` saves `state.db`, `config.yaml`, the memory, the skills, and the scripts to one
`.tar.gz` archive in `~/.tether/backups/`. The archive holds no `.env` and no literal keys. It
holds your messages, so keep it private.

To make a backup each night, add this line to your user crontab (`crontab -e`). Cron does
not read your shell `PATH`, so give the full path of the binary:

```sh
0 3 * * * $HOME/.local/bin/tether backup
```

Each backup is a new file. Delete the old archives in `~/.tether/backups/` from time to time.

To move Tether Agent to a new host, do these steps:

1. Run `tether backup` on the old host.
2. Copy the archive and `~/.tether/.env` to the new host.
3. Run `tether restore FILE` on the new host.
4. Run `systemctl --user start tether`.

Stop the gateway and close `tether chat` before a restore. The restore moves the replaced data
to `~/.tether/pre-restore-<time>/`.

## Install a skill

A skill is a folder with a `SKILL.md` file. The binary has an index of public skills. Show the
index, and filter it with a word:

```sh
tether skills list --available
tether skills list --available pdf
```

Install a skill by its name in the index, or from a local folder, a git URL, or a GitHub repo:

```sh
tether skills install pdf
tether skills install openai/skills --skill playwright
tether skills install ./my-skills
```

The install scans the skill with the skill guard, shows the findings, and asks you to confirm.
The guard does not catch everything, so read the files of a skill before you use it. The
install records the skill in `~/.tether/skills/.hub/lock.json`. A remote install needs the `git`
program. A session that already runs keeps its skill list. Send `/new` to use the new skill.

To see if an installed skill has a newer version at its source, run:

```sh
tether skills check
```

## Architecture

- One binary has two roles: `gateway` runs Telegram and cron, and `chat` runs the CLI.
- Both roles share `state.db` (SQLite in WAL mode) in the data folder `~/.tether`.
- Each agent run is a goroutine: a chat turn, a cron run, a child task, or a review.
- A run calls a provider (OpenAI, Anthropic, or Responses wire format) with the tools of its
  toolsets.
- An approval gate checks dangerous shell commands before they run. A goal gate is checked
  when you add it and again before each run.
- The browser, MCP servers, media programs, and the adapters of the coding agents start on the
  first call and stop when idle.

## Build from source

You need Go 1.26 or later. The build does not use CGO.

To install with Go, run `go install github.com/dnwandana/tether-agent/cmd/tether@latest`. That
binary has the version `dev`. `tether update` then needs `--force`, and `tether doctor` does
not check for a newer release. Use `make build VERSION=v1.2.3` to set a version.

```sh
make build          # bin/tether
make test           # all tests
make test-race      # all tests with the race detector (needs CGO)
make lint           # gofmt, go vet, staticcheck
make cross          # bin/tether-<os>-<arch> for the three release platforms
make test-release   # runs cross and the release checks
make fuzz           # runs each fuzz target for FUZZTIME (default 30s)
make startup-check  # checks that the startup takes less than 100 ms
make soak           # runs the soak test for SOAK_MINUTES (default 5)
```

## Documentation

- [docs/CONFIG.md](docs/CONFIG.md): all configuration keys and their defaults.
- [docs/TOOLS.md](docs/TOOLS.md): the tools and their toolsets.
- [docs/SECURITY.md](docs/SECURITY.md): approvals, protected paths, secrets, and redaction.

## Credits

Tether Agent is inspired by [Hermes Agent](https://github.com/NousResearch/hermes-agent) from
Nous Research. Some parts are ports of Hermes Agent code to Go, for example the approval rules,
the secret redaction, the skill guard, and some prompts. Hermes Agent uses the MIT License. See
[NOTICE](NOTICE) for the list of these parts and the Hermes Agent copyright notice.

## License

Tether Agent uses the MIT License. See [LICENSE](LICENSE).
