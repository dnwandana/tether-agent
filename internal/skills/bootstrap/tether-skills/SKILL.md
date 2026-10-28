---
name: tether-skills
description: How to write a good Tether Agent skill. Use when you create or change a skill with skill_manage, or when a skill_manage write is refused by the skill guard.
---

# Writing skills

A skill is a procedure that you can use again. It is a folder under
`~/.tether/skills/` with a `SKILL.md` file. A folder can be at any level, for example
`skills/deploy/` or `skills/devops/deploy/`.

## Frontmatter

`SKILL.md` starts with YAML frontmatter between two `---` lines:

    ---
    name: deploy
    description: Deploys the web app to the VPS. Use when the user asks to deploy or release.
    required_environment_variables: [DEPLOY_HOST]
    ---

- `name` is required. It must be the same as the folder name.
- `description` is required. The skill index shows the full text, up to 1,024 characters.
- `required_environment_variables` is optional. It lists the env names that the skill
  needs. When you load the skill, these names pass to `terminal` for the session.

## Description style

- First say what the skill does. Then say "Use when" and give the trigger.
- Write the trigger with the words that the user uses.
- Keep the description short. It is in the system prompt of every session.

## Body

- Write the steps as a numbered list.
- Write one lesson per rule. Do not join two lessons in one sentence.
- Give the exact command, path, or setting. Do not give general advice.
- When a step failed in the past, add the error text and the fix.
- Keep `SKILL.md` short. Put long material in `references/`.

## Folders

- `references/` holds long notes, API details, and examples. The body names the file, and
  you read it with `skill_view` and the `file` argument.
- `scripts/` holds helper scripts for the skill.
- Use `skill_manage` with `write_file` to write a file into one of these folders.

## API keys: the script pattern

The skill guard refuses a skill that sends a secret env variable. For example, a `curl`
line that contains a key variable is refused. A declaration in
`required_environment_variables` does not change this. Use a script:

1. Write the API call in a script under `~/.tether/scripts/`, for example
   `~/.tether/scripts/helius.sh`. The script reads the key from its environment.
2. `write_file` and `patch` cannot write in `scripts/`. Ask the user to put the script there,
   or write it with `terminal`. The approval gate then asks the user.
3. List the key name in `required_environment_variables` in the frontmatter.
4. In the skill body, tell the agent to run the script with `terminal`:

       Run `bash ~/.tether/scripts/helius.sh <wallet>`.

The guard scans only the skill folders. It does not scan `~/.tether/scripts/`.

## The skill guard

`skill_manage` scans each new or changed skill before it saves the skill.

- A `dangerous` verdict refuses the write. The result lists the findings. Remove the
  flagged lines, then write the skill again.
- A `caution` verdict saves the skill. The result lists the findings.
- Do not put secrets, tokens, or private keys in a skill.
- Every step must be visible to the user. A skill must not hide a step.

## When to write a skill

- Write a skill after a task that needed many steps, or after a mistake that you corrected.
- Change a skill that exists when it is wrong or incomplete. Do not make a second skill
  for the same task.
- Put a fact about the user or the host in memory, not in a skill. The `tether-memory`
  skill tells where each fact goes.
