---
name: tether-self
description: Tether Agent's own manual. Use when you need to know which tools and toolsets exist, how a config key works, how compaction, cron jobs, delegation, or approvals behave, or when you need a recipe for API calls, PDF files, or a failed web fetch.
---

# Tether Agent manual

Tether Agent is one Go binary. The `gateway` process runs Telegram and cron. The `chat`
process runs the CLI. Both processes share `~/.tether/state.db`.

## Data folder

- `~/.tether/config.yaml` holds the config. A change applies after a restart.
- `~/.tether/.env` holds the secrets. Its mode must be 0600.
- `~/.tether/skills/` holds the skills. The agent reads them again at each session start.
- `~/.tether/scripts/` holds the scripts for cron jobs and for API calls.
- `~/.tether/memory/` holds `memory.md` and `user_profile.md`.

## Tools by toolset

Each tool belongs to one toolset. The config key `toolsets.<channel>` sets the toolsets for
the channels `cli`, `telegram`, and `cron`. The value `all` gives every toolset. A cron job
or a child can only make its set smaller. You do not see a tool that is not in your set.

| Toolset | Tools |
|---|---|
| `terminal` | `run_command` |
| `file` | `read_file`, `write_file`, `patch` |
| `web` | `web_search`, `web_fetch` |
| `browser` | `browser_navigate`, `browser_click`, `browser_fill`, `browser_read`, `browser_screenshot`, `browser_run_js`, `browser_cookies` |
| `vision` | `view_image` |
| `ask` | `ask_user` |
| `memory` | `memory`, `session_search` |
| `skills` | `skill_view`, `skills_list`, `skill_manage` |
| `cron` | `cronjob` |
| `delegation` | `delegate_task` |
| `code_execution` | `run_script` |
| `media` | `text_to_speech`, `transcribe_audio`, `image_generate` |
| `mcp_<server>` | the tools of one MCP server |

## Main config keys

- `providers.primary` and `providers.fallback`: `format`, `base_url`, `api_key`, and `model`.
- `agent.max_turns`: 40 tool rounds in one turn.
- `context.compact_threshold`: 0.50. `context.tool_output_max_chars`: 50000.
- `memory.memory_char_limit`: 2200. `memory.user_char_limit`: 1375.
- `skills.external_dirs`: read-only skill folders. `skills.guard`: true.
- `terminal.default_timeout_s`: 120. `terminal.env_passthrough`: env names that commands see.
- `delegation.max_concurrent_children`: 5. `delegation.child_timeout_s`: 600.
- `approvals.mode`: `manual`. `approvals.timeout_s`: 600. `approvals.cron_mode`: `deny`.
- `cron.max_parallel_jobs`: 2.

## Compaction

- The agent compacts the history when it is larger than 50% of the context window.
- It keeps the system prompt, the first 3 messages, and the last 20 messages.
- A summary from the same model replaces the messages between them.
- The stored transcript does not change. The `/compact` command starts compaction at once.
- Write important facts into memory or a skill. A summary can lose details.

## Cron

Use the `cronjob` tool. Always list the jobs first. Never guess a job ID.

- The schedule can be `30m`, `every 2h`, a cron expression such as `0 1 * * *`, or an ISO time.
- An agent job runs a prompt. The prompt must be complete, because no user is present.
- An optional `script` runs before the prompt. Its stdout goes into the prompt as data.
- A script job has `no_agent: true`. It runs only the script and uses no model.
- A script job sends its stdout word for word. Empty script output sends nothing.
- A script job with a non-zero exit sends an error alert.
- A relative script path is under `~/.tether/scripts/`.
- An agent job always delivers its final answer. With `deliver: local`, it saves the answer only.
- A cron run cannot use `ask_user` and cannot make cron jobs.
- In a cron run, a dangerous command follows `approvals.cron_mode`.

## Delegation

Use `delegate_task` to give a separate task to a child agent.

- Give one `goal`, or a `tasks` list. A list runs in parallel, with a maximum of 5 tasks.
- A child starts with no history. Put all the facts that it needs into `context`.
- A child cannot use `delegate_task`, `ask_user`, `memory`, `run_script`, or `cronjob`.
- A child stops after 50 tool rounds or 600 seconds.
- Set `background: true` to continue your turn. The results come back later.
- You see only the summary of each child.

## Approvals

The approval gate checks each command from `run_command` and `run_script`.

- Some commands are always blocked, for example a delete of the root folder.
- For a dangerous command, the mode `manual` asks the user.
- The mode `smart` asks a model to judge the risk first.
- The mode `off` runs the command with no question.
- The user answers once, for this session, always, or deny.
- No answer in 600 seconds is a deny.
- A denial comes back as `BLOCKED by user: <description>`. Do not try the same command again.
  Ask the user or find a different method.

## Recipes

### API calls

Use `curl` through `run_command`. Show the status code on the last line:

    curl -sS -w '\n%{http_code}' https://api.example.com/v1/items

For a JSON body, write the body to a file first with `write_file`. Then send the file
with `--data @file`. This prevents quoting errors:

    curl -sS -w '\n%{http_code}' -H 'Content-Type: application/json' --data @/tmp/body.json https://api.example.com/v1/items

Do not put a secret key in a skill. Put the call in a script under
`~/.tether/scripts/` and run that script. The `tether-skills` skill tells how.

### PDF files

Download the file with `curl`. Then get the text with `pdftotext`:

    curl -sSL -o /tmp/doc.pdf https://example.com/doc.pdf
    pdftotext -layout /tmp/doc.pdf /tmp/doc.txt

Then read `/tmp/doc.txt` with `read_file`.

### Failed web tools

If web_search or web_fetch fails, use the browser tools. Start with `browser_navigate`, then
use `browser_read`. The browser can read pages that need JavaScript or a login.
