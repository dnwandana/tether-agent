# Skills

A skill is a set of instructions that the agent reads when a task needs it. A skill can teach the
agent a workflow, a tool, or a habit of yours. The agent can write its own skills, and you can
install public skills.

## What a skill is

A skill is a folder with a `SKILL.md` file. The file has YAML frontmatter and a Markdown body.

```markdown
---
name: deploy
description: Deploys the web app to the VPS. Use when the user asks to deploy or release.
required_environment_variables: [DEPLOY_HOST]
---
1. Run `bash ~/.tether/scripts/deploy.sh`.
2. Check the output for the text "deployed".
3. Tell the user the new version.
```

The frontmatter has these fields:

| Field | Required | Effect |
|---|---|---|
| `name` | Yes | The name of the skill. A skill that the agent writes must have the same name as its folder. |
| `description` | Yes | The text that the agent sees in its skill list. Write when to use the skill. The list shows a maximum of 1024 characters. |
| `required_environment_variables` | No | The names of secret variables that the skill needs. When a session loads the skill, these variables go to the `terminal` and `execute_code` tools. |

A skill folder can also have two subfolders:

- `references/` for longer documents that the skill points to.
- `scripts/` for scripts that the skill runs.

A skill body can use two template tokens from Hermes Agent. `skill_view` and a skill command
replace them:

- `${HERMES_SKILL_DIR}` becomes the folder of the skill.
- `${HERMES_SESSION_ID}` becomes the ID of the session.

A support file that the agent reads with `skill_view` keeps its tokens.

## Where skills live

| Location | Content |
|---|---|
| `~/.tether/skills/` | The skills that the agent wrote and the skills that you installed. A skill can be at any folder depth. |
| `skills.external_dirs` | More skill folders. The agent reads them but never writes to them. A skill in `~/.tether/skills/` wins when two skills have the same name. |
| The binary | Three bootstrap skills: `tether-memory`, `tether-self`, and `tether-skills`. The program copies them into `~/.tether/skills/` when they are missing. |

The system prompt lists the name and the description of each skill. The program reads the skills
at the start of each session. Send `/new` to start a new session after you add a skill.

## How the agent uses skills

The agent has three skill tools:

- `skills_list` shows the skills.
- `skill_view` reads a skill.
- `skill_manage` writes skills. Its actions are `create`, `patch`, `write_file`, and `delete`.
  `write_file` can write only in `references/` or `scripts/`.

The file tools cannot write in `~/.tether/skills/`. Only `skill_manage` can write there.

### Background skill review

After a number of tool rounds, a background review reads the conversation again. The default is
10 rounds (`skills.review_interval_tool_rounds`). The review can create or patch a skill, so the
agent learns from the work. You see a short notice after the answer, for example
`🧩 Skill created: deploy`. One review sends one message, with one line for each kind of change,
for example `🧩 Skills updated: deploy, release-notes`.

Set `skills.review_interval_tool_rounds` to `0` to stop the reviews.

## Run a skill with a slash command

Send `/<skill> [request]` to run a skill. For example, `/arxiv transformers 2026` starts a turn
with the full `arxiv` skill and the request "transformers 2026". The command works in Telegram
and in `tether chat`. The CLI prints `⚡ Loading skill: arxiv` before the turn.

The command name comes from the skill name, as in Hermes Agent:

1. The name changes to lower case.
2. Each space and each `_` changes to `-`.
3. Each character other than `a` to `z`, `0` to `9`, and `-` goes away.
4. A run of `-` becomes one `-`. A `-` at the start or at the end goes away.

| Skill name | Command |
|---|---|
| `arxiv` | `/arxiv` |
| `Git Helper` | `/git-helper` |
| `git_helper` | `/git-helper` |
| `pdf.tools` | `/pdftools` |

These rules apply:

- `_` works in place of `-`. `/git_helper` runs the `git-helper` skill. The Telegram menu uses
  this form, because Telegram does not permit `-` in a command name.
- A built-in command always wins. A skill with the name of a built-in command gets no command.
  The names `/exit`, `/quit`, `/start`, `/sethome`, `/approve`, `/deny`, and `/restart` are
  also reserved. The log has a warning for each skill that gets no command.
- When two skills have the same command, a skill in `~/.tether/skills/` wins over an external
  skill. In one group, the first name in sort order wins.
- A skill name with no ASCII letter or digit gets no command. A skill name with a `"` or a
  control character also gets no command.
- A new skill works as a command at once. You do not need `/new`.
- When a task runs, the skill command waits in the queue. The bot replies "⏳ Queued. I'll
  answer after the current task." The skill turn runs after the task. `/stop` and `/new` remove
  it, and `/stop keep` keeps it.
- A channel has skill commands only when the `skill_view` tool is available on that channel. A
  `toolsets` limit that removes the skill tools also removes the skill commands.
- The skill text in the message has the limit of a `skill_view` result,
  `context.tool_output_max_chars`. A longer skill is cut. The full text goes to a file, and the
  message gives the path of the file.
- The skill text stays in the session as a user message. The session title is the command that
  you typed.

To see the skill commands:

- `/help` shows the first 10 skill commands after the built-in commands.
- `/help skills` shows all skill commands.
- The Telegram command menu shows the skill commands after the built-in commands. See
  [Telegram](Telegram#the-command-menu).

## Manage skills with `tether skills`

Run these commands in a terminal on the host.

| Command | Effect |
|---|---|
| `tether skills list` | Shows each skill with its source: local, installed, external, or builtin. |
| `tether skills list --available [WORD]` | Shows the skills in the index of the binary. A word filters by name and description. |
| `tether skills install <source>` | Gets one skill, scans it, and puts it into `skills/`. |
| `tether skills uninstall <name>` | Removes an installed skill and its lockfile entry. |
| `tether skills check [name]` | Compares the installed skills with their sources. It changes nothing. |
| `tether skills update [name...]` | Installs the new version of the skills. With no name, it updates each skill that `check` reports. |
| `tether skills reset [name]` | Restores a bootstrap skill from the binary. With no name, it restores all three. |

### Install sources

| Source | Example |
|---|---|
| A local folder. It starts with `.`, `/`, or `~`. | `tether skills install ./my-skills --yes` |
| A git URL | `tether skills install https://gitlab.com/org/repo` |
| A GitHub tree URL | `tether skills install https://github.com/<owner>/<repo>/tree/<ref>/<path>` |
| A GitHub repo as `owner/repo` | `tether skills install openai/skills --skill playwright` |
| A name from the index | `tether skills install pdf` |

A remote install needs the `git` program.

### Flags

- `--skill NAME` selects one skill when the source has more than one skill.
- `--ref REF` selects a branch, a tag, or a full commit SHA.
- `--yes` confirms with no prompt. It is necessary when stdin is not a terminal.
- `--force` installs a skill again, or passes a `caution` verdict on a community source.
- `--data-dir DIR` selects the data folder.

### Exit codes of `check`

| Code | Meaning |
|---|---|
| `0` | Nothing to do. |
| `1` | An update is available. |
| `2` | No skill could be checked. |

You can run `tether skills check` from a cron script and act on the exit code.

### The skill index

The binary has an index of 75 public skills. The program never downloads the index. A new index
comes only with a new release of Tether Agent.

### The lockfile

`~/.tether/skills/.hub/lock.json` records each installed skill:

- the source
- the commit
- the trust level
- the verdict of the guard
- a hash of the content

`tether skills update --force` saves a skill that the agent changed to `skills/.hub/backup/` before
it replaces the skill.

## The skill guard

The skill guard scans a skill for dangerous content before the agent uses it. The guard has the 121
threat rules of Hermes Agent, in 12 groups:

| Group | Rules |
|---|---|
| exfiltration | 25 |
| injection | 19 |
| obfuscation | 14 |
| persistence | 12 |
| supply_chain | 10 |
| network | 9 |
| destructive | 8 |
| credential_exposure | 6 |
| execution | 6 |
| privilege_escalation | 5 |
| traversal | 5 |
| mining | 2 |

### Verdicts

- A critical finding gives `dangerous`.
- A high finding gives `caution`.
- Medium and low findings alone give `safe`.

The guard is strict. For example, a skill that sends a `$…KEY` variable with `curl` is `dangerous`,
also when the skill declares the variable.

### Structure checks

The guard also checks the structure of the skill folder. Each problem is a finding:

| Check | Severity |
|---|---|
| More than 50 files | medium |
| A file larger than 256 KB | medium |
| More than 1 MB in total | high |
| A file with a binary extension | critical |

The guard also checks executable bits and symbolic links.

### What the guard does for each source

| Source | `dangerous` | `caution` |
|---|---|---|
| A skill that the agent writes | Refused. The findings go back to the agent, so it can write the skill again. | Saved. The findings go into the tool result. |
| A skill in an external folder | Skipped. The program logs a warning. | Loaded. |
| A bootstrap skill | Not scanned. These skills are trusted. | Not scanned. |

`skills.guard: false` turns off these scans. It does not turn off the scan of an install.

### Install policy

`tether skills install` and `tether skills update` always scan in strict mode, also when
`skills.guard` is `false`. A skill with more than 1 MB of text is refused, and no flag passes this
limit.

The trust level comes from the source:

- `trusted`: the GitHub repos `openai/skills`, `anthropics/skills`, `huggingface/skills`, and
  `NVIDIA/skills`.
- `community`: every other source, a local folder too.

| Verdict | community | trusted |
|---|---|---|
| `safe` | You confirm. | You confirm. |
| `caution` | Blocked. `--force` passes it to the confirm step. | You confirm. |
| `dangerous` | Blocked. Only the owner override passes it. | Blocked. Only the owner override passes it. |

### The owner override

To install a `dangerous` skill, you must type the name of the skill. The owner override works only
when stdin and stdout are both terminals. `--yes` and `--force` never pass a `dangerous` verdict.
The lockfile records the owner override.

> [!IMPORTANT]
> The agent can never install, update, or uninstall a skill. The approval gate always blocks
> `tether skills install`, `tether skills update`, and `tether skills uninstall` from the agent.
> No approval mode and no "Always" answer permits them. The agent can run `tether skills list`
> and `tether skills check`, because they change nothing.

## API keys in skills

Do not put an API call with a key in a skill. Use this pattern:

1. Put the API call in a script in `~/.tether/scripts/`.
2. Tell the agent in the skill to run that script with `terminal`.
3. Give the key to the script through `required_environment_variables`.

The [security model](https://github.com/dnwandana/tether-agent/blob/main/docs/SECURITY.md#skill-guard)
gives the full rules.
