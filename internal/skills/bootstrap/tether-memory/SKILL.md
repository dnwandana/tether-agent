---
name: tether-memory
description: Where to keep what you learn in Tether Agent, in memory.md, user_profile.md, or a skill, and the size limits. Use when you save a fact, when a fact does not fit in a memory file, or when you are not sure where a fact goes.
---

# Memory and skills

Tether Agent has three places for what you learn. Use the `memory` tool for the two memory
files. Use `skill_manage` for skills.

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
- An `add` never fails because of the limit. When a fact does not fit in memory.md or
  user_profile.md, the entry goes to a memory note: `inbox-memory` or `inbox-user`.
- Give a `topic` to put related facts in one note. For example, use the topic `rpc` for
  the facts about RPC providers.
- Give no `topic` for a fact that each session needs, for example the name of your user
  or a correction. A note does not load into the system prompt.
- The `## MEMORY TOPICS` section of the system prompt lists the note topics. Use the
  action `search` to find a fact. Use the action `read` to open a note.
- Before you add a fact, `search` the notes for the same subject. When a note has an
  older version of the fact, use `replace`.
- One entry has a limit of 4,000 characters. Write a longer text as several entries.
- Move a long procedure from memory.md into a skill.

## When the agent reads them

- The agent reads memory.md and user_profile.md into the system prompt at the start of a
  session.
- A note does not load into the system prompt. Only its topic shows in `## MEMORY TOPICS`.
- An edit goes to disk at once. A `search` and a `read` show the edit at once.
- The system prompt shows the edit from the next session, or after compaction.
