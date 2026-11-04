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

The final answer of each run goes to the `deliver` target:

| Value | Target |
|---|---|
| empty or `origin` | The chat and topic where you made the job. |
| `local` | No message. The result stays in the database. |
| `telegram:<chat>` | That chat. |
| `telegram:<chat>:<thread>` | That topic in that chat. |

A job that you make in `tether chat` has no Telegram origin. It goes to the home channel. Send
`/sethome` in a Telegram chat to set it, or set `telegram.home_chat` in the config.
`telegram.cron_thread` sets the topic for cron results in the home chat.

If a job has no target, the job still runs, and an agent job still uses tokens. Only the
delivery fails. The job gets the status `delivery_failed`, and the error tells you to run
`/sethome` or to set `deliver`.

> **Note:** `telegram:<chat>` without `:<thread>` does not go to a topic. Add the thread id for a
> forum topic.

## What a run does

An agent run has these rules:

- Each run is a fresh session with the key `cron:<job id>`.
- The run cannot use the `cronjob` tool or the `clarify` tool. No user is present to answer.
- The final answer always goes to the target.
- A failure sends "⚠ Cron job X failed: …".
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
- The timeout is 120 seconds.
- The bot sends stdout word for word, up to 50,000 characters. A longer output is cut, and
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

- The scheduler checks for due jobs every 30 seconds.
- `cron.max_parallel_jobs` (default `2`) limits the jobs that run at the same time. Other due jobs
  wait for the next check.
- Only one job with a `workdir` runs at a time.
- The jobs are in the `cron_jobs` table of `state.db`, so they stay after a restart.
- When the gateway stops during a run, the run ends and its result is not sent. The job list shows
  the status `error` and the stop. The stopped run does not count as one of the `repeat` runs. A
  one-shot job, or a job on its last run, stays due and runs again after the restart. Other jobs
  wait for their next time.

### Missed runs

`cron.missed_runs` sets what happens to a run that the gateway missed while it was stopped:

- `skip` (the default): a job that is late by less than its grace window runs one time. The grace
  window is half the period, from 2 minutes to 2 hours. A later job moves to its next time.
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
