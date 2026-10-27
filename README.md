# Tether Agent

Tether Agent is a personal AI agent in one static Go binary. It works from the CLI, from
Telegram, and from cron jobs. It has tools for the shell, files, the web, a browser, memory,
skills, delegation, MCP servers, and media.

The agent uses little CPU and RAM when it is idle. Heavy parts start on the first call and
stop when they are idle. These parts are the browser, the MCP servers, and the media programs.

## Quick start

1. Download the archive for your platform from the releases page. The platforms are
   linux/amd64, linux/arm64, and darwin/arm64.

   ```sh
   tar -xzf tether-agent_<version>_linux_amd64.tar.gz
   install -m 0755 tether ~/.local/bin/tether
   ```

2. Run the setup wizard. The wizard asks for your provider, an optional fallback provider,
   and an optional Telegram bot. It writes `~/.tether/config.yaml` and
   `~/.tether/.env`.

   ```sh
   tether setup
   ```

3. Run the checks. Each line shows ✓, ⚠, or ✗, and a next action.

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

## Configuration example

This example uses OpenAI as the primary provider and OpenRouter as the fallback provider. Put
the API keys in `~/.tether/.env` (mode 0600). The config refers to them with `${VAR}`.

```yaml
# ~/.tether/config.yaml
providers:
  primary:
    format: openai
    base_url: https://api.openai.com/v1
    api_key: ${OPENAI_API_KEY}
    model: gpt-5.6-sol
  fallback:
    format: openai
    base_url: https://openrouter.ai/api/v1
    api_key: ${OPENROUTER_API_KEY}
    model: openai/gpt-5.6-sol
```

```sh
# ~/.tether/.env
OPENAI_API_KEY=...
OPENROUTER_API_KEY=...
```

The agent uses the fallback provider only when the primary provider fails before the first
byte of the answer. All keys and their default values are in [docs/CONFIG.md](docs/CONFIG.md).

## Architecture

- One binary has two roles: `gateway` runs Telegram and cron, and `chat` runs the CLI.
- Both roles share `state.db` (SQLite in WAL mode) in the data folder `~/.tether`.
- Each agent run is a goroutine: a chat turn, a cron run, a child task, or a review.
- A run calls a provider (OpenAI or Anthropic wire format) with the tools of its toolsets.
- An approval gate checks dangerous shell commands before they run.
- The browser, MCP servers, and media programs start on the first call and stop when idle.

## Build from source

You need Go 1.26 or later. The build does not use CGO.

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
