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
make web                                     # builds web/ into internal/dashboard/dist/ (needs Node)
make web-test                                # the Vitest tests of web/
make web-lint                                # oxlint and the Prettier check of web/
make web-format                              # formats web/ with Prettier
go run ./cmd/tether gen-dashboard-types      # writes web/src/api/types.gen.ts
go run ./cmd/tether gen-dashboard-hash       # writes internal/dashboard/dist/.source-hash (make web runs it)
```

## Architecture

- `cmd/tether/main.go`: `run()` returns an exit code and never calls `os.Exit`, so tests
  call it directly. The subcommands are `chat`, `gateway`, `setup`, `doctor`, `skills`,
  `sessions`, `pairing`, `dashboard`, `usage`, `backup`, `restore`, `update`, `version`, `help`, and the
  hidden `gen-tools-doc`, `gen-dashboard-types`, and `gen-dashboard-hash`.
- `internal/app` is the composition root. `app.New` builds config, DB, providers, tools, MCP,
  and the agent. When `acp.enabled` is true, it also builds the ACP manager (`acp.Manager`) and
  its idle reaper, but it starts no adapter process. Only `Options.Gateway` builds the cron scheduler and the background
  `delegate_task` batches. Only the gateway role polls Telegram.
- `internal/dashboard` is one more gateway channel, `dashboard`. It has its own `net/http`
  server, device sessions, an SSE event hub, and JSON routes. `App.AddDashboard` builds it only
  in the gateway role, and `runGateway` starts the listener.
  The hub keeps the last 30 answered cards in memory (`Hub.Answered`), newest first, with the
  header and the preview redacted and cut at 300 characters. It sends them in
  `Snapshot.Answered` and in each `card.done` event. A restart clears them. The scheduler sends
  `job.run` through `cron.Scheduler.RunEnd`, and the goal manager sends `goal.state` through
  `goals.Deps.Changed`. `app` sets both function fields. Each function reads `a.Dashboard` at
  call time and does nothing when the dashboard is off. So `cron` and `goals` never import
  `dashboard`. The old `goal` event (`{"line"}`) of the chat goal line does not change.
  `web/` holds the page (Vue, TypeScript, Tailwind CSS, and shadcn-vue components on Reka UI).
  Run `make web` before `make build`, because git keeps only the placeholder
  `internal/dashboard/dist/.keep`. `TestDashboardTypesCurrent` fails when `types.gen.ts` is
  old. `TestDashboardAssetsCurrent` fails with "Run make web" when the built page is older than
  `web/`. Both tests skip when no page is built.
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
     requests of the coding agent of `acp_delegate` go through `security.Gate.AskACP`. An MCP tool
     call that is not `read` goes through `security.Gate.CheckMCP` (through
     `tools.MCPApprover`). In read-only mode, `Registry.Dispatch` refuses each call that the
     read-only allow list does not allow.
  6. `provider` streams the answer (`openai`, `anthropic`, or `responses` wire format). Each
     model call goes through a `provider.Chain` from `provider.Set.ChainFor`. The chain order
     is the pinned entry, `primary`, and then `agent.fallback`. The chain goes to the next
     entry only on an error that wraps `provider.ErrBeforeFirstByte`. `provider.Health` keeps
     the failures and skips a failed entry for `provider.failure_skip_s` (default 60 s). All chains
     share one `Health`.
- State: the data dir is `--data-dir`, else `TETHER_HOME`, else `~/.tether`. `state.db` is
  pure-Go SQLite (`modernc.org/sqlite`) with one connection. Do not run a second query while
  rows are open, because it deadlocks. Migrations are `internal/db/migrations/NNN_*.sql`
  (embedded, applied in name order). `approval_grants` holds the Always answers, `chat_modes`
  holds the `/mode` value of each scope, and `approval_log` holds the decisions. `Gate.ModeOf`
  queries `chat_modes` at each decision, so close all rows first. The gateway holds a `flock` on `gateway.lock`.
- Import rules: `tools` cannot import `agent` (`tools.ChildSpecLite` mirrors
  `agent.ChildSpec`), and `gateway` cannot import `app` (`app` builds `gateway.Deps`).
  `goals` cannot import `gateway`, `agent`, or `app`. `app` gives it the provider, cost, and
  batch functions. `internal/acp` imports only `config`, `security`, `db`, the SDK
  `github.com/coder/acp-go-sdk`, and the standard library. `tools` reaches it through the
  `tools.ACPDelegator` interface. `internal/brief` imports only `cron`, `goals`, `obs`, `db`,
  `sessions`, and the standard library. `cron` cannot import `brief`, `gateway`, or `app`. `internal/dashboard` cannot import `app`,
  `telegram`, or `setup`. `gateway` cannot import `dashboard`. `internal/media` cannot import
  `gateway`.
- The browser, MCP servers, media programs, and ACP adapters (`claude-agent-acp`,
  `codex-acp`) start on the first call and stop when idle. `app.New` must not do network work, because `TestStartupUnder100ms` measures startup. For
  example, `provider.ModelCache` makes no call before the first `/model <entry>`.

## Rules that span files

- Keep the system prompt of a session byte-stable. The provider prompt cache needs the same
  bytes. `agent.PromptCache` keys a prompt by session, model override, prompt override,
  compaction count, MCP generation, and mode. Memory and skill edits do not change a prompt that already exists.
- A new tool implements `tools.Tool` and registers in `App.registerTools` (or
  `registerDelegation`). Then do these steps:
  1. Put the tool in a toolset from `internal/tools/toolsets.go`. A new set also goes into
     `AllToolsets()`.
  2. Check the allow lists and block lists: `agent.ChildBlockedTools`,
     `agent.ReviewAllowedTools`, `tools.ScriptCallableTools`, and `toolNeeds` in
     `internal/tools/doc.go`.
  3. Put the tool in the read-only allow list or the deny list of `internal/tools/readonly.go`,
     or give it a `ReadOnlyCall` method. `TestEachToolIsInOneReadOnlyList` in `internal/tools`
     fails until the tool is in one list.
  4. Generate `docs/TOOLS.md` again. `TestGeneratedDocIsCurrent` fails when it is out of date.
- A change to a struct in an `internal/dashboard/api*.go` file needs
  `go run ./cmd/tether gen-dashboard-types` and then `make web`. A named string type with a fixed
  set of values, for example `dashboard.SetupState`, registers its values with `registerEnum` in
  `internal/dashboard/gentypes.go`. The generator then writes a string union in `types.gen.ts`,
  for example `type SetupState = "done" | "open" | "unknown"`. A Go test checks that the
  constants match the registry.
- A change in `web/` must pass `make web-lint` (oxlint and Prettier) and `make web-test`.
  `make web-format` fixes the format. Prettier skips `web/src/api/types.gen.ts`, because
  `TestDashboardTypesCurrent` compares it byte for byte with the generator output.
- `web/src/components/ui/` holds shadcn-vue code that the shadcn-vue CLI copies. Load the
  `shadcn-vue` skill before you change `web/`. To add a component, run
  `npx shadcn-vue@2.9.0 add <name>` in `web/`. Then make sure that each version in
  `web/package.json` stays exact. The copied files are part of the source, so keep them in the
  repository.
- Colors come from the tokens in `web/src/styles/app.css`, for example `bg-primary` and
  `text-muted-foreground`. Do not use a Tailwind palette class such as `bg-blue-500`.
- The component and page tests of `web/` find elements by role, label, and text, with
  `@testing-library/vue`.
- `web/src/styles/app.css` keeps the classes `.scroll`, `.aside-slot`, `.announce`, and `.pulse`,
  because code outside the templates reads them.
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
     `telegram.encodeCmd` cuts a longer action. Keep each action at most `ActionMaxBytes`
     bytes (`internal/gateway/modellist.go`). The `/model` buttons also use `modelEntryMaxBytes`.
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
- Tests reset global state: `security.AddProtectedPathPatterns(nil)`, `security.SetHumanOnlyRules(nil)`,
  `security.SetBoundSecretNames(nil)`, the command table of `gateway.Register`, and
  `gateway.pickTip` and `gateway.nowFunc`.
- `internal/skills/testdata/guard_golden.json` is the output of the Hermes threat patterns.
  No tool regenerates it.

## Docs to sync

A change updates its docs in the same change. A spec has a "Docs" section with one row for each
place that changes: the file, the `path:line` or the heading, the text today, and the change. Write
"no change" for each doc family with no hit. Find the places with a search, not from memory:

1. List the terms that the change adds, renames, or removes: commands, flags, config keys, tool
   names, user-facing texts, file names, and behaviors.
2. Search each term and the old text of each changed behavior in each doc family below.
3. Search all tracked files last: `git ls-files | xargs grep -nE '<pattern>'`. For a short term
   such as `now`, use a pattern with boundaries, for example `(^|[^a-z-])/now\b`.
4. If the change renames a heading in `docs/wiki/`, search the wiki for links to its anchor
   (`#<heading-slug>`).

| Doc family | Files | Update it when |
| --- | --- | --- |
| README | `README.md` | The feature list, the install, the quick start, or a command example changes. |
| Agent rules | `AGENTS.md` | The architecture, the message flow, a command, a package rule, or a rule that spans files changes. |
| Config | `docs/CONFIG.md` | A config key, its default, or a preset changes. |
| Tools | `docs/TOOLS.md` | A tool changes. Generate it with `go run ./cmd/tether gen-tools-doc`. |
| Security | `docs/SECURITY.md` | The approval gate, a protected path, a secret, or a sandbox rule changes. |
| Wiki | `docs/wiki/*.md` | The behavior that a page describes changes. A new page also goes into `_Sidebar.md`. |
| Help text | `cmd/tether/help.go`, the command table of `gateway.Register`, `helpTelegram` and `helpCLI` in `internal/gateway/commands.go`, the Telegram `BotCommands` | A command, a flag, or its description changes. |
| Web dashboard | `web/src/` | A command, a setting, or a behavior that the page shows changes. |
| Text that the agent reads | `internal/agent/base_prompt.md`, `internal/skills/bootstrap/*/SKILL.md` | A tool, a command, or a behavior that the agent tells the user about changes. |
| Credits | `NOTICE` | The change ports code from Hermes or from another project. |

Code comments change with their code.

## Writing style

- Write comments, docs, and user-facing strings in ASD-STE100 Simplified Technical English,
  like the existing text.
- Do not cite planning notes in committed text: no section numbers, feature codes such as
  `G2.5`, or task numbers. Those notes are not in the repository. State the fact directly.
