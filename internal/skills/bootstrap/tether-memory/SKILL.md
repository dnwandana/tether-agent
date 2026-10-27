---
name: tether-memory
description: Where to keep what you learn in Tether Agent, in memory.md, user_profile.md, or a skill, and the size limits. Use when you save a fact, when a memory file is full, or when you are not sure where a fact goes.
---

# Memory and skills

Tether Agent has three places for what you learn. Use the `memory` tool for the two memory
files. Use `skill_manage` for skills.

## memory.md (target `memory`)

- It holds your own notes and facts about the environment.
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
- When a file is full, an `add` is refused. Then merge entries before you add one.
  Merge two related entries into one with `replace`, or `remove` an old entry.
- Move a long procedure from memory.md into a skill.

## When the agent reads them

- The agent reads both files into the system prompt at the start of a session.
- An edit goes to disk at once.
- The system prompt shows the edit from the next session, or after compaction.
