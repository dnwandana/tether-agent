---
name: tether-memory
description: Where to keep what you learn in Tether Agent, in rules.md, memory.md, user_profile.md, or a skill, and the size limits. Use when you save a fact or a rule of your user, when a fact does not fit in a memory file, or when you are not sure where a fact goes.
---

# Memory and skills

Tether Agent has four places for what you learn. Use the `memory` tool for the three memory
files. Use `skill_manage` for skills.

## rules.md (target `rule`)

- It holds the standing rules of your user. The `## OWNER RULES` section of the system prompt
  shows them first.
- When your user states a standing rule ("always", "never", "from now on", "do not ... unless"),
  save it with target `rule`. Put the exact words of your user in `quote`. A rule write with
  no quote from this session is refused.
- The limit is 1,000 characters. A full rules.md refuses a new rule. It does not go to a note.
  Ask your user which rule to remove.
- Do not give a `topic` to a rule.

## memory.md (target `memory`)

- It holds your own facts: the lessons that you learn and the facts about the environment.
- Examples: the host OS, the tools that are installed, the project paths, and a command
  that works on this host.
- The limit is 2,200 characters.

## user_profile.md (target `user`)

- It holds facts about your user. Save the name of your user here first.
- Examples: the name, the time zone, the preferred language, and the style of answers.
- The limit is 1,375 characters.

## A skill

- It holds a procedure: the steps for a task that you can do again.
- A skill has no size limit like the memory files, but keep it short.
- The `tether-skills` skill tells how to write one.

## Rules

- Write one fact per entry.
- Write a fact only when it will help in a later session.
- Do not save a fact that is true for this task only.
- Do not save secrets, tokens, or passwords.
- When a fact changes, use `replace`. Do not add a second entry.
- An `add` to memory.md or user_profile.md never fails because of the limit. When the fact
  does not fit, the entry goes to a memory note: `inbox-memory` or `inbox-user`.
- Give a snapshot an `until` date. A snapshot is true only for a time, for example a version or
  a plan for this week. Use the field `until` with a date `YYYY-MM-DD`, at most 366 days ahead.
  The entry stops loading after that date.
- Read the similar entries in the result of an add. When an old entry is no longer true,
  replace it or remove it.
- When you save what your user said, put the exact words in `quote`. A write to the user
  profile from a review needs it.
- Use the action `move` to move one entry between a memory file and a note. Set `to` to
  `note` or `file`.
- A write after outside content (a web page, a browser page, an MCP tool, or a subagent result)
  waits for your user. The result starts with "Held for the owner:". Do not try again, and do
  not write the text to a file.
- Give a `topic` to put related facts in one note. For example, use the topic `rpc` for
  the facts about RPC providers.
- A note has a limit of 1 MiB. When a note is full, an `add` puts the entry in the next
  note of the topic, for example `inbox-memory-2`. The result names the note.
- Give no `topic` for a fact that each session needs, for example the name of your user
  or a correction. A note does not load into the system prompt.
- The `## MEMORY TOPICS` section of the system prompt lists the note topics. Use the
  action `search` to find a fact. Use the action `read` to open a note.
- Before you add a fact, `search` the notes for the same subject. When a note has an
  older version of the fact, use `replace`.
- One entry has a limit of 4,000 characters by default (`memory.entry_max_chars`). Write a longer text as several entries.
- Move a long procedure from memory.md into a skill.

## When the agent reads them

- The agent reads rules.md, memory.md, and user_profile.md into the system prompt at the start
  of a session. An entry whose `until` date is past does not load.
- A note does not load into the system prompt. Only its topic shows in `## MEMORY TOPICS`.
- An edit goes to disk at once. A `search` and a `read` show the edit at once.
- The system prompt shows the edit from the next session, or after compaction.
