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
## When to Use
The user asks to deploy or release the web app.

## Procedure
1. Run `bash ~/.tether/scripts/deploy.sh`.
2. Tell the user the new version.

## Pitfalls
- Run the script from the home folder, because it uses relative paths.

## Verification
The output has the text "deployed".
```

The frontmatter has these fields:

| Field | Required | Effect |
|---|---|---|
| `name` | Yes | The name of the skill. A skill that the agent writes must have the same name as its folder. |
| `description` | Yes | The text that the agent sees in its skill list. Write when to use the skill. The list shows a maximum of 1024 characters. |
| `required_environment_variables` | No | The names of the variables that the skill needs. A skill passes no variable. `exec_secrets` binds a secret to a command. `terminal.env_passthrough` passes a name to each command. `skill_view` shows one status for each name: `passed`, `bound: <commands>`, `not passed: add it to terminal.env_passthrough in config.yaml`, or `not set`. |

The body has four sections in this order: `## When to Use` (the requests that call for the
skill), `## Procedure` (numbered steps with the exact command, path, or setting), `## Pitfalls`
(one rule for each lesson, with one clause of why), and `## Verification` (how to check that the
work is done). The bundled `tether-skills` skill tells the agent to use them. An update does not
change an installed copy of a bundled skill. Run `tether skills reset` once to get the new text of
`tether-skills`, `tether-memory`, and `tether-self`.

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
| `~/.tether/skills/.history/` | A copy of a skill before each change of `skill_manage`. The newest 5 copies of each skill stay (`skills.history_keep`). |
| `~/.tether/skills/.archive/` | The skills that the skill curator archived. |

The program skips each folder whose name starts with `.`, so a copy or an archived skill is not
in the skill list.

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
agent learns from the work. You see one card after the answer with each change of the review,
and an ↩ Undo button. The review has these limits:

- Before each change of `skill_manage`, the program copies the skill folder to
  `skills/.history/`. `/skills log` shows the copies, and `/skills undo` restores one.
- A review makes at most 3 skill changes (`skills.review_max_changes`). The next change is
  refused before it runs.
- A review cannot change a bundled skill or a skill from the hub. A chat turn can, when you ask.
- A review cannot create a skill when a similar skill exists or is archived. It patches the
  existing skill. A chat turn can create it, and the result names the similar skills.
- The review writes each pitfall as one rule with one clause of why, with no PR numbers, no
  dates, and no quoted user text.

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

- `/help` shows the first 10 skill commands (`gateway.help_max_skills`) after the built-in commands.
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
| `tether skills stale` | Shows the skills that the curator can archive, with the last activity and the state: `active` or `stale`. |
| `tether skills keep <name>` | Marks a skill as kept. The curator never archives it. |
| `tether skills unkeep <name>` | Removes the keep mark. |
| `tether skills restore <name>` | Moves an archived skill back to its old folder. It refuses when a skill with the name exists. |
| `tether skills archived` | Shows the archived skills. |
| `tether skills log [name]` | Shows the last 10 skill copies with their copy IDs. |
| `tether skills undo <copy id>` | Undoes one skill change from its copy. |
| `tether skills curate` | Runs the curator now, also when it is not due. It prints the result and the restore commands. |

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
- `--force` installs a skill again, or passes a `caution` verdict on a community source. A file that the guard cannot scan also gives a `caution` verdict, so it needs `--force`.
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

## The skill curator

A skill that nobody reads costs prompt bytes in each session. The skill curator archives such a
skill. It makes no model call, and it never deletes a skill.

The program records each read of a skill: a `skill_view` call in a chat turn, a skill command,
and a cron run that loads the skill. The reads and the copies of a review do not count.

The curator runs only in the gateway. It runs when `skills.curator.interval_hours` (168) passed
since the last run and no chat had a message for `skills.curator.min_idle_hours` (2). The first
run only starts the clock. A skill is a candidate when all of these are true:

- It is in `~/.tether/skills/`, not in `skills.external_dirs`.
- It is not a bundled skill, and it is not installed from the hub.
- It is not kept (`tether skills keep` or `/skills keep`).
- No cron job names it in its skills or as a word in its prompt, also a paused job.
- Its folder holds no other skill.

A candidate with no activity for `skills.curator.stale_after_days` (14) is stale. A candidate
with no activity for `skills.curator.archive_after_days` (30) moves to `skills/.archive/`. The
activity is the newest read, the newest change from a chat turn or from you, or the first time
that the curator saw the skill.

After a run that archived a skill, you get a 🗂 Skill curator card in Telegram, with
↩ Restore all, Restore one ›, and ✓ OK. A `skill_view` of an archived skill answers that it is
archived, so the agent does not write it again. Send `/skills restore <name>` or run
`tether skills restore <name>` to get it back. Set `skills.curator.enabled` to `false` to stop
the curator.

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

The guard scans every file of the skill, whatever its extension: a script with no extension can
also run. It always scans `SKILL.md`, the known text types, each file under `scripts/`, each file
that starts with `#!`, and each other file with no NUL byte. A file that starts with an ELF, PE,
or Mach-O header is critical. The guard does not honor a `.skillignore` or `.clawhubignore` file
in the skill folder, because the author of the skill wrote it.

The guard also scans a file with a NUL byte when bash can run it, that is, when the first line in
the first 80 bytes has no NUL byte. It skips such a file when the file is under `.git/`, or when
it is a real media file: an image, font, PDF, audio, or video file with the correct magic bytes
for its type. An archive, for example a `.zip` file, is never a media file. The guard deletes each
NUL byte before the scan, as bash does. A file that the guard reads only by this rule gets no
check for invisible characters, because random bytes give false findings. `SKILL.md`, the text
types, and the files under `scripts/` keep the check, also with a NUL byte.

The guard cannot scan some files. Examples are a binary file or an archive that is not a real
media file. The guard gives such a file the high finding `unscanned_file`. The verdict is
`caution`, because a program can run the file. The guard gives no finding for a file under `.git/`.
A file that already has the finding `binary_file` does not also get `unscanned_file`.
The report lists a file that it did not scan.

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
| `caution` | Blocked. `--force` passes it to the confirm step. This includes a file with the finding `unscanned_file`. | You confirm. |
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
2. Give the script a `#!` line and the exec bit (`chmod +x`).
3. Tell the agent in the skill to run that script with `terminal`, by its absolute path and
   alone, for example `/home/you/.tether/scripts/helius.sh <wallet>`. `bash <script>`, a pipe,
   or a `cd` before the script gets no key.
4. Bind the name of the key in `exec_secrets` in `config.yaml` to the absolute path of the script.

The [security model](https://github.com/dnwandana/tether-agent/blob/main/docs/SECURITY.md#skill-guard)
gives the full rules.
