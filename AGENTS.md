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
  call it directly. The subcommands are `chat`, `gateway`, `setup`, `doctor`, `skills`,
  `sessions`, `pairing`, `usage`, `backup`, `restore`, `update`, `version`, `help`, and the
  hidden `gen-tools-doc`.
- `internal/app` is the composition root. `app.New` builds config, DB, providers, tools, MCP,
  and the agent. When `acp.enabled` is true, it also builds the ACP manager (`acp.Manager`) and
  its idle reaper, but it starts no adapter process. Only `Options.Gateway` builds the cron scheduler and the background
  `delegate_task` batches. Only the gateway role polls Telegram.
- Message flow:
  1. `telegram.Channel` or `gateway.CLI` receives the message.
  2. `gateway.Gateway` runs one turn per session key. On a busy key, a plain text goes into
     the running turn as a steer (`agent.SteerQueue`), and other messages wait in the queue.
  3. `agent.Agent.RunTurn` runs the loop. It saves each message as soon as the message exists.
  4. When the session has an active goal, `goals.Manager` runs the after-turn step: the wait
     check, the gates, and the judge. On `continue`, the gateway adds a continuation turn to
     the end of the queue. The CLI runs it at once.
  5. `tools.Registry.Dispatch` runs the tool calls. The `terminal` tool goes through
     `security.Gate.Check` (the approval gate). The script text of `execute_code` goes through
     `security.Gate.CheckScript` (through `tools.ScriptApprover`). The permission
     requests of the coding agent of `acp_delegate` go through `security.Gate.AskACP`.
  6. `provider` streams the answer (`openai`, `anthropic`, or `responses` wire format). Each
     model call goes through a `provider.Chain` from `provider.Set.ChainFor`. The chain order
     is the pinned entry, `primary`, and then `agent.fallback`. The chain goes to the next
     entry only on an error that wraps `provider.ErrBeforeFirstByte`. `provider.Health` keeps
     the failures and skips a failed entry for 60 s. All chains share one `Health`.
- State: the data dir is `--data-dir`, else `TETHER_HOME`, else `~/.tether`. `state.db` is
  pure-Go SQLite (`modernc.org/sqlite`) with one connection. Do not run a second query while
  rows are open, because it deadlocks. Migrations are `internal/db/migrations/NNN_*.sql`
  (embedded, applied in name order). The gateway holds a `flock` on `gateway.lock`.
- Import rules: `tools` cannot import `agent` (`tools.ChildSpecLite` mirrors
  `agent.ChildSpec`), and `gateway` cannot import `app` (`app` builds `gateway.Deps`).
  `goals` cannot import `gateway`, `agent`, or `app`. `app` gives it the provider, cost, and
  batch functions. `internal/acp` imports only `config`, `security`, `db`, the SDK
  `github.com/coder/acp-go-sdk`, and the standard library. `tools` reaches it through the
  `tools.ACPDelegator` interface.
- The browser, MCP servers, media programs, and ACP adapters (`claude-agent-acp`,
  `codex-acp`) start on the first call and stop when idle. `app.New` must not do network work, because `TestStartupUnder100ms` measures startup. For
  example, `provider.ModelCache` makes no call before the first `/model <entry>`.

## Rules that span files

- Keep the system prompt of a session byte-stable. The provider prompt cache needs the same
  bytes. `agent.PromptCache` keys a prompt by session, model override, prompt override,
  compaction count, and MCP generation. Memory and skill edits do not change a prompt that already exists.
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
- A slash command replies with a `gateway.Reply` through `CommandCtx.send`. The text is common
  Markdown. Do these steps:
  1. Escape user text, for example a title, a model name, or an error, with
     `gateway.EscapeMarkdown`. Keep `markdownEscapes` in `internal/gateway/markdown.go` and
     `mdEscapes` in `internal/telegram/format.go` the same.
  2. Check `c.interactive()`. The CLI shows no buttons, so give it a line with the command to
     send.
  3. Keep a button action short. Telegram callback data has a limit of 64 bytes, and
     `telegram.encodeCmd` cuts a longer action. The `/model` buttons stay under the limit
     through `modelEntryMaxBytes` and `modelActionMaxBytes` in `internal/gateway/modellist.go`.
     A tap runs the action as a typed command through `Gateway.Submit`.
- A new provider preset needs these steps:
  1. Add a row to `presets` in `internal/config/presets.go`. The table order is the wizard
     order.
  2. Add a row to the preset table in `docs/CONFIG.md`. No test checks that table.
  3. Add the ID to the two ID lists of `TestPresetTable` in `internal/config/presets_test.go`.
  4. Give the row an `EffortStyle` that `mapEffort` in `internal/provider/effort.go` handles.
     `mapEffort` maps an unknown style like `chat`.
- All Telegram sends and edits of one chat share one budget, `telegram.ChatPace`. A new send
  or edit goes through a typed method of `telegram.Client` that has the chat id.
  `Client.Call` gives no chat, so its calls skip the budget. Select the class of the call:
  1. A direct reply to the user, for example a command reply, an ack, or a prompt, uses
     `replyClient`. It waits for the 1 s slot and the flood end, but not for the group window.
  2. A send with no turn, for example a cron delivery, keeps the default class. It also
     waits for the group window.
  3. A status or preview message uses `queueClient` or `interimClient`. A full budget gives a
     `*paceHold` at once. A hold is not a flood and not a try.
- Tests reset global state: `security.AddProtectedPathPatterns(nil)`, the command table of
  `gateway.Register`, and `gateway.pickTip` and `gateway.nowFunc`.
- `internal/skills/testdata/guard_golden.json` is the output of the Hermes threat patterns.
  No tool regenerates it.

## Writing style

- Write comments, docs, and user-facing strings in ASD-STE100 Simplified Technical English,
  like the existing text.
- Do not cite planning notes in committed text: no section numbers, feature codes such as
  `G2.5`, or task numbers. Those notes are not in the repository. State the fact directly.
