# Tools

<!-- tether gen-tools-doc writes this file. Do not edit it. Run:
     go run ./cmd/tether gen-tools-doc > docs/TOOLS.md -->

This file lists the built-in tools of Tether Agent by toolset. Each entry shows
the description and the JSON schema of the parameters that the model sees.

- `toolsets.<channel>` in config.yaml selects the toolsets of each channel. A tool
  outside these toolsets is hidden from the model.
- The MCP tools are not in this file. Each MCP server adds its tools at run time in the
  toolset `mcp_<server>`.
- Some tools need a host program or a config value. The entry of such a tool has a
  **Needs** line.

| Toolset | Tools |
| --- | --- |
| `ask` | `clarify` |
| `browser` | `browser_click`, `browser_cookies`, `browser_fill`, `browser_navigate`, `browser_read`, `browser_run_js`, `browser_take_screenshot` |
| `code_execution` | `execute_code` |
| `cron` | `cronjob` |
| `delegation` | `delegate_task` |
| `file` | `patch`, `read_file`, `write_file` |
| `media` | `generate_image`, `text_to_speech`, `transcribe_audio` |
| `memory` | `memory`, `session_search` |
| `skills` | `skill_manage`, `skill_view`, `skills_list` |
| `terminal` | `terminal` |
| `vision` | `view_image` |
| `web` | `web_fetch`, `web_search` |

## ask

### clarify

Ask the user a question and wait for the answer. Use it when the task is ambiguous, when a decision has trade-offs that the user must choose, or to get feedback. Give choices for a multiple-choice question; the user can also type a different answer. Omit choices for an open question. Do not use it to confirm dangerous commands: the terminal tool asks for those itself. When the decision is small, make a reasonable choice yourself.

```json
{
  "type": "object",
  "properties": {
    "question": {
      "type": "string",
      "description": "The question to the user."
    },
    "choices": {
      "type": "array",
      "items": {
        "type": "string"
      },
      "maxItems": 10,
      "description": "Optional answer choices. Omit them for an open question."
    },
    "multi_select": {
      "type": "boolean",
      "description": "Let the user pick more than one choice."
    }
  },
  "required": [
    "question"
  ]
}
```

## browser

### browser_click

Click the first element that matches a selector on the current page. A selector that starts with "/" is XPath; any other selector is CSS.

**Needs:** Chrome or Chromium (browser.chrome_path, CHROME_PATH, or PATH). Without it, the tool returns an error.

```json
{
  "type": "object",
  "properties": {
    "selector": {
      "type": "string",
      "description": "A CSS selector, or an XPath expression that starts with \"/\"."
    }
  },
  "required": [
    "selector"
  ]
}
```

### browser_cookies

Read, set, or delete browser cookies. get: return the cookies of the current page, or of a domain. set: set the cookie name=value for the current page, or for a domain. clear: delete the cookie name, or all cookies of the current page or of a domain when you give no name.

**Needs:** Chrome or Chromium (browser.chrome_path, CHROME_PATH, or PATH). Without it, the tool returns an error.

```json
{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "enum": [
        "get",
        "set",
        "clear"
      ],
      "description": "The action to do."
    },
    "name": {
      "type": "string",
      "description": "The cookie name. Required for set. Optional for get and clear."
    },
    "value": {
      "type": "string",
      "description": "The cookie value for set."
    },
    "domain": {
      "type": "string",
      "description": "Optional. The cookie domain, for example example.com. The default is the domain of the current page."
    }
  },
  "required": [
    "action"
  ]
}
```

### browser_fill

Type text into the first input or textarea that matches a selector. The tool clears the field first. A selector that starts with "/" is XPath; any other selector is CSS.

**Needs:** Chrome or Chromium (browser.chrome_path, CHROME_PATH, or PATH). Without it, the tool returns an error.

```json
{
  "type": "object",
  "properties": {
    "selector": {
      "type": "string",
      "description": "A CSS selector, or an XPath expression that starts with \"/\"."
    },
    "text": {
      "type": "string",
      "description": "The text to type."
    }
  },
  "required": [
    "selector",
    "text"
  ]
}
```

### browser_navigate

Open a URL in the headless browser and wait for the page and its JavaScript. Returns the title, the final URL, and the first 2000 characters of the page text. Call it before the other browser tools. For plain pages, web_fetch is faster; use the browser for pages that need JavaScript, a login, clicks, or forms. The browser keeps cookies and logins between calls and restarts.

**Needs:** Chrome or Chromium (browser.chrome_path, CHROME_PATH, or PATH). Without it, the tool returns an error.

```json
{
  "type": "object",
  "properties": {
    "url": {
      "type": "string",
      "description": "The http or https URL to open."
    }
  },
  "required": [
    "url"
  ]
}
```

### browser_read

Return the visible text (innerText) of the current page, or of the first element that matches a selector. With no selector, the tool returns the text of the whole body.

**Needs:** Chrome or Chromium (browser.chrome_path, CHROME_PATH, or PATH). Without it, the tool returns an error.

```json
{
  "type": "object",
  "properties": {
    "selector": {
      "type": "string",
      "description": "Optional. A CSS selector, or an XPath expression that starts with \"/\"."
    }
  }
}
```

### browser_run_js

Run JavaScript in the current page and return the result as JSON. The value of the last expression is the result. The tool waits for a promise. Use it to read data from the page or to do what the other browser tools cannot do.

**Needs:** Chrome or Chromium (browser.chrome_path, CHROME_PATH, or PATH). Without it, the tool returns an error.

```json
{
  "type": "object",
  "properties": {
    "code": {
      "type": "string",
      "description": "The JavaScript code. Example: document.querySelectorAll('a').length"
    }
  },
  "required": [
    "code"
  ]
}
```

### browser_take_screenshot

Take a PNG screenshot of the current page. Returns MEDIA:<path>. Use view_image with the path to inspect the image. Put the MEDIA:<path> tag in your answer to send the image to the user.

**Needs:** Chrome or Chromium (browser.chrome_path, CHROME_PATH, or PATH). Without it, the tool returns an error.

```json
{
  "type": "object",
  "properties": {
    "full_page": {
      "type": "boolean",
      "description": "Capture the full page, not only the visible part. The default is false."
    }
  }
}
```

## code_execution

### execute_code

Run a Python script that calls tools in a loop, and get back only what the script prints. Use it for mechanical multi-step work: many fetches, file edits in a loop, or filtering large tool output. The intermediate tool results never enter your context.

Import the tools from the tether_tools module, for example:
  from tether_tools import web_fetch, ToolError
  page = web_fetch(urls=['https://example.com'])
Each function takes the same arguments as the tool, returns the result text, and raises ToolError when the call fails. The callable tools are web_search, web_fetch, read_file, write_file, patch, terminal, when they are available in this session.

LIMITS: 300 s, 50 tool calls, and 50000 bytes of stdout. The script runs in a scratch folder without secrets in its environment. Print only the result that you need.

**Needs:** python3 on PATH. Without it, the tool is not registered.

```json
{
  "type": "object",
  "properties": {
    "code": {
      "type": "string",
      "description": "The Python 3 script."
    },
    "timeout_s": {
      "type": "integer",
      "description": "The timeout in seconds. The maximum and the default is 300."
    }
  },
  "required": [
    "code"
  ]
}
```

## cron

### cronjob

Manage scheduled cron jobs with a single compressed tool.

Use action='create' to schedule a new job from a prompt or one or more skills.
Use action='list' to inspect jobs.
Use action='update', 'pause', 'resume', 'remove', or 'run' to manage an existing job.

To stop a job the user no longer wants: first action='list' to find the job_id, then action='remove' with that job_id. Never guess job IDs — always list first.

Jobs run in a fresh session with no current-chat context, so prompts must be self-contained.
If skills are provided on create, the future cron run loads those skills in order, then follows the prompt as the task instruction.
On update, passing skills=[] clears attached skills.

NOTE: The agent's final response is auto-delivered to the target on every run. Put the primary
user-facing content in the final response. Cron jobs run autonomously with no user
present — they cannot ask questions or request clarification.

SCRIPT JOBS (no_agent=true): the scheduler runs the script with no model call and sends its stdout word for word. Empty stdout sends nothing, so make the script quiet when there is nothing to report. A failure (non-zero exit or timeout) always alerts.

Important safety rule: cron-run sessions should not recursively schedule more cron jobs.

```json
{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "enum": [
        "create",
        "list",
        "update",
        "pause",
        "resume",
        "remove",
        "run"
      ],
      "description": "One of: create, list, update, pause, resume, remove, run. When action=create, the 'schedule' field and a 'prompt' (or no_agent with a script) are REQUIRED."
    },
    "job_id": {
      "type": "string",
      "description": "Required for update/pause/resume/remove/run. Take it from action='list'."
    },
    "prompt": {
      "type": "string",
      "description": "For create: the full self-contained prompt. If skills are also provided, this becomes the task instruction paired with those skills."
    },
    "schedule": {
      "type": "string",
      "description": "REQUIRED for action=create. For create/update: '30m', 'every 2h', '0 9 * * *', or ISO timestamp. Examples: '30m' (every 30 minutes), 'every 2h' (every 2 hours), '0 9 * * *' (daily at 9am), '2026-06-01T09:00:00' (one-shot). The time zone is the host time zone."
    },
    "name": {
      "type": "string",
      "description": "Optional human-friendly name"
    },
    "repeat": {
      "type": "integer",
      "description": "Optional repeat count. Omit for defaults (once for one-shot, forever for recurring)."
    },
    "deliver": {
      "type": "string",
      "description": "Omit this parameter to auto-deliver back to the current chat and topic (recommended). Only set it when the user asks to deliver somewhere else. Values: 'origin' (same as omitting), 'local' (no delivery, save only), or telegram:<chat_id>[:<thread_id>] for a specific destination, for example 'telegram:-1001234567890:17585'. WARNING: 'telegram:<chat_id>' without :<thread_id> loses topic targeting."
    },
    "skills": {
      "type": "array",
      "items": {
        "type": "string"
      },
      "description": "Optional ordered list of skill names to load before executing the cron prompt. On update, pass an empty array to clear attached skills."
    },
    "model": {
      "type": "object",
      "description": "Optional per-job model override. If provider is omitted, the current main provider is pinned at creation time so the job stays stable.",
      "properties": {
        "provider": {
          "type": "string",
          "description": "Provider name from the providers config. Omit to use and pin the current provider."
        },
        "model": {
          "type": "string",
          "description": "Model name"
        }
      },
      "required": [
        "model"
      ]
    },
    "script": {
      "type": "string",
      "description": "Optional path to a script. A relative path resolves under ~/.tether/scripts/. .sh and .bash files run with bash, other files with python3. In an agent job, the script runs first and its stdout goes into the prompt as data. With no_agent=true, the script IS the job: empty stdout sends nothing; a failure always alerts. On update, pass an empty string to clear."
    },
    "no_agent": {
      "type": "boolean",
      "description": "Default false: the agent runs the prompt each tick. Set true to skip the model: the scheduler runs the script and delivers its stdout word for word, with zero tokens. Requires script."
    },
    "toolsets": {
      "type": "array",
      "items": {
        "type": "string"
      },
      "description": "Optional list of toolset names to restrict the job's agent to (e.g. [\"web\", \"terminal\", \"file\"]). On update, pass an empty array to clear."
    },
    "workdir": {
      "type": "string",
      "description": "Optional absolute path of an existing folder to run the job from. On update, pass an empty string to clear."
    }
  },
  "required": [
    "action"
  ]
}
```

## delegation

### delegate_task

Spawn one or more subagents to work on tasks in isolated contexts. Each subagent gets its own conversation and toolsets. Only the final summary is returned; intermediate tool results never enter your context window.

TWO MODES (one of 'goal' or 'tasks' is required):
1. Single task: provide 'goal' (+ optional context, toolsets).
2. Batch (parallel): provide a 'tasks' array with up to 5 items (delegation.max_concurrent_children). All run in parallel and the results are returned together. Nested delegation is OFF (max_spawn_depth=1): every child is a leaf and cannot delegate further. Raise delegation.max_spawn_depth in config.yaml to enable nesting.

LIMITS: each child has at most 50 tool rounds and 600 s. The limits come from config.yaml.

BACKGROUND: set background to true to start the children and go on at once. The tool returns a batch_id. When every child is done, a message '[Background delegation <batch_id> finished]' with the results arrives as a new user turn. /stop cancels the running background children.

WHEN TO USE delegate_task:
- Reasoning-heavy subtasks (debugging, code review, research synthesis)
- Tasks that would flood your context with intermediate data
- Parallel independent workstreams (research A and B simultaneously)

WHEN NOT TO USE:
- Mechanical multi-step work with no reasoning needed: use execute_code
- Single tool call: call the tool directly
- Tasks needing user interaction: subagents cannot use clarify

IMPORTANT:
- Subagents have NO memory of your conversation. Pass all relevant info (file paths, error messages, constraints) in the 'context' field.
- If the user writes in a language other than English, or asks for a specific language or style, say so in 'context'.
- Subagent summaries are SELF-REPORTS, not verified facts. For operations with external side effects, require a verifiable handle (URL, ID, absolute path) and verify it yourself before you tell the user it succeeded.
- Subagents CANNOT call: delegate_task (unless role='orchestrator'), clarify, memory, execute_code, cronjob.
- Results are always returned as an array, one entry per task.

**Needs:** the gateway role for background: true. The chat role runs the tasks in the foreground.

```json
{
  "properties": {
    "background": {
      "description": "Start the children in the background and return a batch_id at once. Default false.",
      "type": "boolean"
    },
    "context": {
      "description": "Background information the subagent needs: file paths, error messages, project structure, constraints.",
      "type": "string"
    },
    "goal": {
      "description": "What the subagent should accomplish. Be specific and self-contained: the subagent knows nothing about your conversation history.",
      "type": "string"
    },
    "role": {
      "description": "Role of the child. 'leaf' (default) is a focused worker that cannot delegate further. 'orchestrator' can use delegate_task to spawn its own workers. Nesting is OFF (max_spawn_depth=1); 'orchestrator' is silently forced to 'leaf'.",
      "enum": [
        "leaf",
        "orchestrator"
      ],
      "type": "string"
    },
    "tasks": {
      "description": "Batch mode: tasks to run in parallel (up to 5, set by delegation.max_concurrent_children). When provided, the top-level goal, context, and toolsets are ignored.",
      "items": {
        "properties": {
          "context": {
            "description": "Task-specific context",
            "type": "string"
          },
          "goal": {
            "description": "Task goal",
            "type": "string"
          },
          "role": {
            "description": "Per-task role override",
            "enum": [
              "leaf",
              "orchestrator"
            ],
            "type": "string"
          },
          "toolsets": {
            "description": "Toolsets for this task",
            "items": {
              "type": "string"
            },
            "type": "array"
          }
        },
        "required": [
          "goal"
        ],
        "type": "object"
      },
      "type": "array"
    },
    "toolsets": {
      "description": "Toolsets for the subagent. They can only narrow your own toolsets. Default: your toolsets. Available: terminal, file, web, browser, vision, ask, memory, skills, cron, delegation, code_execution, media, mcp.",
      "items": {
        "type": "string"
      },
      "type": "array"
    }
  },
  "type": "object"
}
```

## file

### patch

Replace old_string with new_string in a file, and return a unified diff of the change. The match first tries the exact text, then tolerates whitespace and indentation drift per line. After a drift match, new_string gets the indentation of the file. When old_string matches more than one place, the tool changes nothing and lists the lines; add context or set replace_all.

```json
{
  "type": "object",
  "properties": {
    "path": {
      "type": "string",
      "description": "The file path. A relative path starts at the session working folder."
    },
    "old_string": {
      "type": "string",
      "description": "The text to replace. Include enough lines to make it unique."
    },
    "new_string": {
      "type": "string",
      "description": "The replacement text."
    },
    "replace_all": {
      "type": "boolean",
      "description": "Replace every match. The default is false."
    }
  },
  "required": [
    "path",
    "old_string",
    "new_string"
  ]
}
```

### read_file

Read a text file. The result has numbered lines: the line number, a tab, then the line. One call returns at most 2000 lines and 2000 characters per line. When more lines follow, the result ends with next_offset; call again with that offset to read more.

```json
{
  "type": "object",
  "properties": {
    "path": {
      "type": "string",
      "description": "The file path. A relative path starts at the session working folder."
    },
    "offset": {
      "type": "integer",
      "description": "The first line to read, starting at 1. The default is 1."
    },
    "limit": {
      "type": "integer",
      "description": "The maximum number of lines to read. The default and the maximum are 2000."
    }
  },
  "required": [
    "path"
  ]
}
```

### write_file

Write the full content of a file. The tool creates missing parent folders. To replace an existing file, call read_file on it first in this session. The tool refuses the write when the file changed after that read. Use patch for small edits.

```json
{
  "type": "object",
  "properties": {
    "path": {
      "type": "string",
      "description": "The file path. A relative path starts at the session working folder."
    },
    "content": {
      "type": "string",
      "description": "The full new content of the file."
    }
  },
  "required": [
    "path",
    "content"
  ]
}
```

## media

### generate_image

Generate an image from a text prompt. Returns MEDIA:<path>. Put this tag in your answer to send the image to the user.

**Needs:** media.image. It has no default backend, so the tool is not registered until you set one.

```json
{
  "type": "object",
  "properties": {
    "prompt": {
      "type": "string",
      "description": "A detailed description of the image."
    },
    "size": {
      "type": "string",
      "description": "Optional. The image size, for example 1024x1024."
    }
  },
  "required": [
    "prompt"
  ]
}
```

### text_to_speech

Convert text to speech audio. Returns MEDIA:<path> [[audio_as_voice]]. Put this tag in your answer to send the audio to the user; Telegram sends it as a voice note.

**Needs:** media.tts. The default command needs edge-tts. With an empty backend, the tool is not registered.

```json
{
  "type": "object",
  "properties": {
    "text": {
      "type": "string",
      "description": "The text to speak."
    },
    "voice": {
      "type": "string",
      "description": "Optional. The voice name for an OpenAI-format endpoint, for example alloy or nova. A command backend uses the voice of its command."
    }
  },
  "required": [
    "text"
  ]
}
```

### transcribe_audio

Transcribe an audio file (speech to text). Returns the text.

**Needs:** media.stt. The default command needs whisper-cli. With an empty backend, the tool is not registered.

```json
{
  "type": "object",
  "properties": {
    "path": {
      "type": "string",
      "description": "The path of the audio file, for example a voice note. A relative path starts at the work folder."
    }
  },
  "required": [
    "path"
  ]
}
```

## memory

### memory

Save durable facts to persistent memory. Memory goes into the system prompt of later sessions, so keep it short and keep only facts that stay useful.

WHEN TO SAVE (do it without a request):
- The user corrects you, or says 'remember this' or 'do not do that again'.
- The user tells you a preference, a habit, or a personal detail (name, role, time zone, coding style).
- You find a fact about the environment (OS, installed tools, project structure).
- You learn a convention, an API quirk, or a workflow of this setup.
- You find a stable fact that is useful again in later sessions.

PRIORITY: user preferences and corrections first, then environment facts, then procedures. The best memory stops the user from saying the same thing again.

Do NOT save task progress, session results, logs of finished work, or temporary TODO state. Use session_search to find those in old sessions. When you find a new way to do a task, save it as a skill with skill_manage.

TWO TARGETS:
- 'user': who the user is: name, role, preferences, communication style, dislikes.
- 'memory': your notes: environment facts, project conventions, tool quirks, lessons.

ACTIONS: add (a new entry), replace (change the entry that old_text finds), remove (delete the entry that old_text finds). The memory has a character limit. When an add goes over the limit, the error lists the entries; merge or remove one first.

SKIP: trivial facts, facts that are easy to find again, raw data, and temporary task state.

```json
{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "enum": [
        "add",
        "replace",
        "remove"
      ],
      "description": "The action to do."
    },
    "target": {
      "type": "string",
      "enum": [
        "memory",
        "user"
      ],
      "description": "The store: 'memory' for your notes, 'user' for the user profile."
    },
    "content": {
      "type": "string",
      "description": "The entry text. Required for add and replace."
    },
    "old_text": {
      "type": "string",
      "description": "A short unique part of the entry to replace or remove."
    }
  },
  "required": [
    "action",
    "target"
  ]
}
```

### session_search

Search the messages of past sessions, or read in one session. The search uses SQLite FTS5 and returns real messages, not summaries.

THREE SHAPES:
1) DISCOVERY: pass query (and limit). Each hit gives the session id, the title, the date, the message id, and a snippet with the match in [brackets]. The newest hits come first.
2) READ: pass session_id only. Returns the messages of the session from the start.
3) SCROLL: pass session_id and around_message_id (and window). Returns the message and the window messages before and after it. To scroll forward, pass the last message id; to scroll back, pass the first.

FTS5 SYNTAX: all words must match by default. Use OR for more results (alpha OR beta), quotes for an exact phrase ("docker networking"), NOT to exclude a word, and * for a prefix (deploy*).

Use this tool first for questions like 'what did we do about X' or 'where did we stop with Y'.

```json
{
  "type": "object",
  "properties": {
    "query": {
      "type": "string",
      "description": "The FTS5 query (discovery shape)."
    },
    "limit": {
      "type": "integer",
      "description": "Discovery shape: the maximum number of hits. The default is 5, the maximum is 20."
    },
    "session_id": {
      "type": "string",
      "description": "Read and scroll shapes: the session to read."
    },
    "around_message_id": {
      "type": "integer",
      "description": "Scroll shape: the message id at the center of the window."
    },
    "window": {
      "type": "integer",
      "description": "Scroll shape: the number of messages on each side. The default is 10, the maximum is 50."
    }
  }
}
```

## skills

### skill_manage

Create, change, or delete your own skills. A skill is a SKILL.md file with YAML frontmatter (name, description) and a markdown body with the instructions. Save a skill when you find a way to do a task that is useful again.

ACTIONS:
- create: name and content (the full SKILL.md). The frontmatter name must be the same as name.
- patch: name, old_string, new_string. Replaces one exact match in SKILL.md, or in file when you give it.
- write_file: name, file, content. Writes a support file under references/ or scripts/.
- delete: name.

The skill guard scans each write. It refuses a skill that sends secrets out, for example curl with $API_KEY. Put an API call in a script under ~/.tether/scripts/ (write_file cannot write there: ask the user, or use the terminal tool), and make the skill run that script with the terminal tool. Skills from external folders are read-only.

```json
{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "enum": [
        "create",
        "patch",
        "write_file",
        "delete"
      ],
      "description": "The action to do."
    },
    "name": {
      "type": "string",
      "description": "The skill name: lowercase letters, digits, '.', '_', and '-'."
    },
    "content": {
      "type": "string",
      "description": "create: the full SKILL.md. write_file: the file content."
    },
    "old_string": {
      "type": "string",
      "description": "patch: the exact text to replace. It must match once."
    },
    "new_string": {
      "type": "string",
      "description": "patch: the new text."
    },
    "file": {
      "type": "string",
      "description": "write_file and patch: a path under references/ or scripts/, for example references/api.md."
    }
  },
  "required": [
    "action",
    "name"
  ]
}
```

### skill_view

Load a skill: its SKILL.md instructions and the list of its support files. Load a skill before you do a task that it covers. To read a support file, pass its path in the skill folder as file, for example references/api.md. When the skill needs environment variables, the terminal tool gets them in this session after the view.

```json
{
  "type": "object",
  "properties": {
    "name": {
      "type": "string",
      "description": "The skill name from the skills index or skills_list."
    },
    "file": {
      "type": "string",
      "description": "A support file in the skill folder, for example references/api.md. Omit it to load SKILL.md."
    }
  },
  "required": [
    "name"
  ]
}
```

### skills_list

List the installed skills with their descriptions. Load one with skill_view.

```json
{
  "type": "object",
  "properties": {}
}
```

## terminal

### terminal

Run a shell command with bash -c on the host. The result is stdout and stderr together, followed by the exit code when it is not 0. Set background to true for servers and long jobs: the tool then returns the pid and a log path at once. Read the log with read_file and stop the process with kill.

```json
{
  "type": "object",
  "properties": {
    "command": {
      "type": "string",
      "description": "The shell command to run."
    },
    "timeout_s": {
      "type": "integer",
      "description": "The timeout in seconds. The default is 120."
    },
    "workdir": {
      "type": "string",
      "description": "The working folder. A relative path starts at the session working folder."
    },
    "background": {
      "type": "boolean",
      "description": "Start the command in the background and return its pid and log path at once."
    }
  },
  "required": [
    "command"
  ]
}
```

## vision

### view_image

Look at an image with the vision model and get a text answer. Give a local file path (for example the path of an [image: <path>] marker or a browser screenshot) or an http(s) URL. Ask a specific question to get a specific answer.

**Needs:** a vision provider: agent.vision_provider, or vision: true on the primary provider. Without it, the tool is not registered.

```json
{
  "type": "object",
  "properties": {
    "path": {
      "type": "string",
      "description": "A local image file. A relative path starts at the working folder."
    },
    "url": {
      "type": "string",
      "description": "An http or https image URL. Use path or url, not both."
    },
    "question": {
      "type": "string",
      "description": "The question about the image. The default is \"Describe this image in detail.\""
    }
  }
}
```

## web

### web_fetch

Fetch web pages with a plain HTTP GET and return the main text of each page as markdown. No JavaScript runs; for pages that need JavaScript, use the browser tools. Max 5 URLs per call. PDF files are not supported; download them with curl and convert them with pdftotext.

```json
{
  "type": "object",
  "properties": {
    "urls": {
      "type": "array",
      "items": {
        "type": "string"
      },
      "maxItems": 5,
      "description": "The http or https URLs to fetch."
    },
    "max_chars": {
      "type": "integer",
      "description": "The maximum number of characters for each page. The default is no extra limit."
    }
  },
  "required": [
    "urls"
  ]
}
```

### web_search

Search the web with DuckDuckGo. The result is a numbered list with the title, the URL, and a snippet of each hit. Use web_fetch to read a result page.

```json
{
  "type": "object",
  "properties": {
    "query": {
      "type": "string",
      "description": "The search query."
    },
    "limit": {
      "type": "integer",
      "description": "The number of results. The default is 5 and the maximum is 10."
    }
  },
  "required": [
    "query"
  ]
}
```
