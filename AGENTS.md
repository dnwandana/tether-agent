# AGENTS.md

This file gives guidance to coding agents that work in this repository.

Tether Agent is a personal AI agent in one static Go binary (Go 1.26, `CGO_ENABLED=0`). It
works from the CLI, from Telegram, and from cron. Many behaviors copy Hermes Agent, the
open-source agent that Tether Agent follows. "Hermes" in a comment points to the source of a
rule or a text.

Naming has three forms:

- Prose (docs, comments, prompts, and skills) says `Tether Agent`.
- The repository, the Go module, and the release archives are `tether-agent`.
- Everything at run time is `tether`: the binary (`cmd/tether`), the commands
  (`tether setup`), the data dir (`~/.tether`), and the systemd unit (`tether.service`).

## Commands

```sh
make build                                   # bin/tether
make test                                    # CGO_ENABLED=0 go test ./...
make lint                                    # gofmt -l, go vet, staticcheck
CGO_ENABLED=0 go test ./internal/agent/ -run '^TestName$' -v   # one test
go test -short ./...                         # skips the startup test, which builds a binary
make fuzz FUZZTIME=30s                       # the targets in FUZZ_TARGETS of the Makefile
go run ./cmd/tether gen-tools-doc > docs/TOOLS.md
```

## Architecture

- `cmd/tether/main.go`: `run()` returns an exit code and never calls `os.Exit`, so tests
  call it directly. The subcommands are `chat` (the default), `gateway`, `setup`, `doctor`,
  `skills`, `sessions`, `pairing`, `usage`, and `version`.
- `internal/app` is the composition root. `app.New` builds config, DB, providers, tools, MCP,
  and the agent. Only `Options.Gateway` builds the cron scheduler and the background
  `delegate_task` batches. Only the gateway role polls Telegram.
- Message flow:
  1. `telegram.Channel` or `gateway.CLI` receives the message.
  2. `gateway.Gateway` runs one turn per session key. On a busy key, a plain text goes into
     the running turn as a steer (`agent.SteerQueue`), and other messages wait in the queue.
  3. `agent.Agent.RunTurn` runs the loop. It saves each message as soon as the message exists.
  4. `tools.Registry.Dispatch` runs the tool calls. `run_command` goes through
     `security.Gate.Check` (the approval gate).
  5. `provider` streams the answer (`openai` or `anthropic` wire format). The fallback
     provider runs only when the primary fails before the first byte.
- State: the data dir is `--data-dir`, else `TETHER_HOME`, else `~/.tether`. `state.db` is
  pure-Go SQLite (`modernc.org/sqlite`) with one connection. Do not run a second query while
  rows are open, because it deadlocks. Migrations are `internal/db/migrations/NNN_*.sql`
  (embedded, applied in name order). The gateway holds a `flock` on `gateway.lock`.
- Import rules: `tools` cannot import `agent` (`tools.ChildSpecLite` mirrors
  `agent.ChildSpec`), and `gateway` cannot import `app` (`app` builds `gateway.Deps`).
- The browser, MCP servers, and media programs start on the first call and stop when idle.
  `app.New` must not do network work, because `TestStartupUnder100ms` measures startup.

## Rules that span files

- Keep the system prompt of a session byte-stable. The provider prompt cache needs the same
  bytes. `agent.PromptCache` keys a prompt by session, overrides, compaction count, and MCP
  generation. Memory and skill edits do not change a prompt that already exists.
- A new tool implements `tools.Tool` and registers in `App.registerTools` (or
  `registerDelegation`). Then do these steps:
  1. Put the tool in a toolset from `internal/tools/toolsets.go`. A new set also goes into
     `AllToolsets()`.
  2. Check the allow lists and block lists: `agent.ChildBlockedTools`,
     `agent.ReviewAllowedTools`, `tools.ScriptCallableTools`, and `toolNeeds` in
     `internal/tools/doc.go`.
  3. Generate `docs/TOOLS.md` again. `TestGeneratedDocIsCurrent` fails when it is out of date.
- A new config key goes into the structs and `Default()` in `internal/config/config.go`. Add
  its row to `docs/CONFIG.md`. `internal/config/docs_test.go` compares each yaml key and its
  default with that table.
- Tests reset global state: `security.AddProtectedPathPatterns(nil)` and the command table of
  `gateway.Register`.
- `internal/skills/testdata/guard_golden.json` is the output of the Hermes threat patterns.
  No tool regenerates it.

## Writing style

- Write comments, docs, and user-facing strings in ASD-STE100 Simplified Technical English,
  like the existing text.
- Do not cite planning notes in committed text: no section numbers, feature codes such as
  `G2.5`, or task numbers. Those notes are not in the repository. State the fact directly.
