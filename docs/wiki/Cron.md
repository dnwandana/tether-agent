# Cron

Tether Agent can run jobs on a schedule. A job can run the agent with a prompt, or it can run a
script with no model call. The gateway runs the jobs and sends the results to Telegram.

## Before you start

- Run the gateway. Only `tether gateway` runs the scheduler. See
  [Running as a Service](Running-as-a-Service).
- Set up Telegram, so that the results have a target. See [Telegram](Telegram).

## Create a job

There is no `tether cron` subcommand and no job list in the config. You ask the agent, and the
agent uses the `cronjob` tool.

Example requests:

- "Every day at 9am, summarize the top posts on Hacker News."
- "Every 2 hours, check the disk space on this server and tell me if it is above 80%."
- "Remind me in this topic at 2026-11-01T09:00 to renew the domain."
- "List my cron jobs."
- "Show the last runs of the disk job."
- "Pause the Hacker News job."
- "Remove the disk job."

The agent always lists the jobs before it changes one. It does not guess job ids.

A job runs in a fresh session with no context from the current chat. The agent writes a prompt
that contains all the information that the job needs.

## Actions

| Action | Effect |
|---|---|
| `create` | Makes a new job. It needs a `schedule` and a `prompt` (or a `script` with `no_agent`). |
| `list` | Shows the jobs. |
| `update` | Changes a job. |
| `pause` | Stops a job until you resume it. |
| `resume` | Starts a paused job again. |
| `remove` | Deletes a job. |
| `run` | Runs a job now. |
| `runs` | Shows the last runs of a job: the status, the delivery, the duration, and the output. Ask for older runs, and the agent pages back. |

## Job fields

| Field | Effect |
|---|---|
| `prompt` | The full task for the agent. |
| `schedule` | When the job runs. See [Schedule syntax](#schedule-syntax). |
| `name` | A name that you can read. |
| `repeat` | The number of runs. The default is one run for a one-shot job and no limit for a recurring job. |
| `deliver` | The target of the result. See [Delivery](#delivery). |
| `skills` | Skills that the job loads in order before the prompt. |
| `model` | A `provider` and a `model` for this job. See [Provider and reasoning effort](#provider-and-reasoning-effort). |
| `script` | A script in the `scripts/` folder. See [Script jobs](#script-jobs). |
| `no_agent` | `true` runs only the script, with no model call. |
| `draft_only` | `true` makes the job draft-only: it can read and write drafts, but it cannot send. See [Draft-only jobs](#draft-only-jobs). |
| `toolsets` | A smaller list of toolsets for this job. |
| `workdir` | An absolute path of a folder to run the job from. |

## Schedule syntax

All times use the time zone of the host. The systemd unit that `tether setup` writes sets `TZ`
from the host.

| Form | Example | Meaning |
|---|---|---|
| Interval | `30m`, `2h`, `1d`, `every 2h` | Runs again after each interval. The range is 1 minute to 366 days. |
| Cron expression | `0 9 * * *` | A standard 5-field expression. This example runs every day at 09:00. |
| ISO time | `2026-11-01T09:00` | Runs one time. A time in the past is refused. |

## Delivery

The final answer of a run goes to the `deliver` target. A `[SILENT]` answer sends nothing. See
[What a run does](#what-a-run-does).

| Value | Target |
|---|---|
| empty or `origin` | The chat and topic where you made the job. |
| `local` | No message. The result stays in the database. A pause after too many failures goes to the home channel. See [Failures in a row](#failures-in-a-row). |
| `telegram:<chat>` | That chat. |
| `telegram:<chat>:<thread>` | That topic in that chat. |

A job that you make in `tether chat` has no Telegram origin. It goes to the home channel. Send
`/sethome` in a Telegram chat to set it, or set `telegram.home_chat` in the config.
`telegram.cron_thread` sets the topic for cron results in the home chat.

If a job has no target, the job still runs, and an agent job still uses tokens. Only the
delivery fails. The job gets the status `delivery_failed`, and the error tells you to run
`/sethome` or to set `deliver`. A delivery failure alone does not count as a failed run. See
[Failures in a row](#failures-in-a-row).

> **Note:** `telegram:<chat>` without `:<thread>` does not go to a topic. Add the thread id for a
> forum topic.

A delivery to a group, a topic, or a private chat of a member is scanned. The scan masks a secret,
a card number, an IBAN, and a phone number. The owner gets a note with the button
`✅ Post the full text there` for 24 h. See [Security](Security#outbound-scan) for the scan.

## Held deliveries

When a run read outside content (web, browser, MCP, or a network command) and delivers to a target
that is not private, `model_guard` checks the purpose of the job and the action. A hold puts the
delivery in a card in your private chat. The card has the buttons ❌ Drop and ✅ Send, and it waits
for 24 h. A local-only job makes no extra model call. `outbound.guard` and
`outbound.guard_provider` set the check. See [Security](Security#model_guard-and-draft-only-jobs).

## Draft-only jobs

A draft-only job can read and write drafts, but it cannot send. These rules apply to the run:

- The run has no `terminal`, `execute_code`, `browser_click`, `browser_fill`, `browser_run_js`,
  `browser_cookies`, `delegate_task`, or `acp_delegate`.
- The run can call only MCP read tools and draft tools.
- The job delivers only to a private chat or to `local`.
- An alert of the job goes to your private chat.
- The job cannot use `no_agent`.

Example prompt: "Every morning at 8, read new mail and write draft replies. Make it draft-only."

## What a run does

An agent run has these rules:

- Each run is a fresh session with the key `cron:<job id>`.
- The stdout of the data script goes into the prompt as untrusted data, in an
  `untrusted_tool_result` block.
- A long stdout of the data script goes to the spill file
  `cache/spill/cron_data_script-<id>.txt`. A `read_file` of that file wraps the text in the same
  block, with the source `cron data script`.
- The `runs` action shows the delivery value of each run: `held`, `sent`, `dropped`, or `expired`.
- The run cannot use the `cronjob` tool or the `clarify` tool. No user is present to answer.
- The final answer goes to the target. When the run finds nothing new, the agent answers
  `[SILENT]`, and no message goes out. The run is still recorded.
- A failure sends "⚠ Cron job X failed: …". A time-out sends "⚠ Cron job X failed (timed out
  after Ns)". When the model wrote a part of its answer before the time-out, the part follows
  the alert.
- A failed run always sends its alert to the target, also when the answer has `[SILENT]`. A
  `local` job has no target.
- When the data script fails, the run always delivers. The model reports the failure.
- An empty answer is a failure. It sends "⚠ Cron job X finished, but the model gave no text."
- An agent run has no time limit by default. Set `cron.agent_timeout_s` to stop a run that takes
  too long. The data script does not count.
- The toolsets come from `toolsets.cron`. The `toolsets` field of a job can only make that list
  smaller.

Nobody can answer an approval during a cron run. `approvals.cron_mode` sets the result for a
dangerous command:

- `deny` (the default): the command does not run.
- `approve`: the command runs.

This also applies to the subagents of a cron job. See [Security](Security).

### Provider and reasoning effort

A job with no `model` field uses the primary provider. The `model` field pins a provider entry
and a model for the runs of one job:

- `model` with a `provider` pins that provider entry.
- `model` with no `provider` pins the provider entry of the session that made the job. That is
  the `/model` value of the session, else `primary`.
- The `model` value replaces the model of the pinned provider entry only.
- An unknown provider entry uses `primary`. The gateway log shows a warning.
- An `update` with an empty `model` object removes the pin.

Each run uses the fallback chain. The chain starts with the pinned provider entry, then
`primary`, then the fallback list in `agent.fallback`. See
[Providers](Providers#the-fallback-chain).

A cron run has no `/effort` value, and a job has no field for the reasoning effort. Each call
uses the `effort` key of the provider entry that answers. With no `effort` key, the provider
default applies. See [Providers](Providers#reasoning-effort).

## Script jobs

A script job runs a script and sends its output word for word. It makes no model call, so it uses
zero tokens. Use it for checks that run often.

The script has these rules:

- The script must be in the `scripts/` folder of the data dir. A relative path starts in that
  folder. A path outside that folder is refused, because a script job runs with no approval.
- `.sh` and `.bash` files run with `bash`. Other files run with `python3`.
- The timeout is 120 seconds. `cron.script_timeout_s` changes it.
- The bot sends stdout word for word, up to 50,000 characters (`cron.script_output_max_chars`). A longer output is cut, and
  the full text goes to a spill file.
- Empty stdout sends nothing. Make the script quiet when there is nothing to report.
- A failure always sends an alert. A failure is a non-zero exit code or a timeout.

The agent cannot write to `scripts/`, because it is a protected path. You put the script there.

### Example

1. Write the script to `~/.tether/scripts/disk.sh`:

   ```sh
   #!/bin/sh
   use=$(df --output=pcent / | tail -1 | tr -dc '0-9')
   if [ "$use" -gt 80 ]; then
     echo "Disk / is at ${use}%."
   fi
   ```

2. Ask the agent: "Make a script job that runs disk.sh every 30 minutes, with no agent."

The agent makes a job with `script: disk.sh`, `no_agent: true`, and `schedule: 30m`. You get a
message only when the disk is more than 80% full.

In an agent job, a `script` runs first, and its stdout goes into the prompt as data.

## The scheduler

- The scheduler checks for due jobs every 30 seconds (`cron.tick_s`).
- `cron.max_parallel_jobs` (default `2`) limits the jobs that run at the same time. Other due jobs
  wait for the next check.
- Only one job with a `workdir` runs at a time.
- The jobs are in the `cron_jobs` table of `state.db`, so they stay after a restart.
- When the gateway stops during a run, the run ends and its result is not sent. The job list shows
  the status `stopped`. The stopped run does not count as one of the `repeat` runs. A one-shot
  job, or a job on its last run, stays due and runs again one time after the restart. When a
  second stop in a row stops that run too, the run counts, and the job does not run again. The
  job list shows the cause. An edit or a resume of the job gives it one rerun again. Other jobs
  wait for their next time.
- When the gateway cannot record a run in `state.db`, the job waits before it runs again. The
  wait starts at 1 minute and doubles each time, up to 1 hour. The log shows the error and the
  job id.
- When the stored schedule of a job is not valid, the gateway pauses the job after its run. The
  job list shows the cause. Update the schedule, then resume the job.
- The `cron_runs` table of `state.db` keeps the last 50 runs of each job (`cron.run_history`):
  the status, the delivery, the duration, the output (up to 4000 characters), and the session.
  The redactor masks the secrets in the output before the table keeps it.
  Ask the agent for the runs of a job. The `runs` action shows them.

### Failures in a row

A job that fails 10 runs in a row (`cron.fail_streak_limit`) pauses itself. The alert of the 10th
failure ends with "⏸ The job X (id) is paused: it failed 10 runs in a row. Fix the cause. Then ask
me to resume the job."

A good run sets the count to 0. A resume or an edit of the task (the prompt, the script, the
skills, the model, the toolsets, the workdir, or the schedule) also sets it to 0. When the alert
cannot be sent, the job does not pause. The next failure tries the alert again. A delivery failure
alone does not count. A stopped run does not change the count.

A one-shot job and a job on its last repeat do not pause, because they stop after the run. The job
list shows the count, for example "paused (10 failures in a row)".

### Missed runs

`cron.missed_runs` sets what happens to a run that the gateway missed while it was stopped:

- `skip` (the default): a job that is late by less than its grace window runs one time. The grace
  window is half the period, from 2 minutes to 2 hours (`cron.grace_min_s` and `cron.grace_max_s`). A later job moves to its next time.
- `catchup`: each due job runs one time, also when it missed more than one run.

A one-shot job that is late always runs one time.

## Jobs from the CLI

`tether chat` also has the `cronjob` tool. The gateway sees a new job at its next check. In
`tether chat`, the `run` action does not run the job. It marks the job as due, and the gateway
runs it at its next check.

## See also

- [Telegram](Telegram#the-home-channel): the home channel.
- [Tools](Tools): the toolsets that a job can use.
- [docs/TOOLS.md](https://github.com/dnwandana/tether-agent/blob/main/docs/TOOLS.md#cronjob): the
  full `cronjob` tool reference.
- [docs/CONFIG.md](https://github.com/dnwandana/tether-agent/blob/main/docs/CONFIG.md#cron): the
  `cron` keys.
