# Goals

With `/goal`, the agent works toward a goal over several turns. After each turn, a judge
model reads the answer. When the goal is not done, the next turn starts at once. The loop
stops when the judge says that the goal is done, when the judge says that the goal is
blocked, or when the goal uses all its turns. Tether Agent copies the `/goal` command of
Hermes Agent.

The agent can also keep a task list for work with many steps. See
[The task list](#the-task-list).

## Set a goal

Send `/goal` and the goal text:

```text
/goal Make the cron tests pass 20 times in a row
```

The reply starts with "⊙ Goal set (20-turn budget): Make the cron tests pass 20 times in a
row", and the first goal turn starts at once. A new goal replaces the old goal. It also
removes the subgoals and the gates of the old goal.

When the goal text starts with a command word, for example `pause`, put `--` first:
`/goal -- pause the old cron jobs`.

## The commands

| Command | Effect |
|---|---|
| `/goal`, `/goal status` | Shows the status line of the goal. |
| `/goal show` | Shows the status line and the completion contract. |
| `/goal <text>` | Sets a new goal and starts the first goal turn. |
| `/goal -- <text>` | Sets a goal whose text starts with a command word. |
| `/goal draft <text>` | Asks the judge model to write a contract. Then it sets the goal and starts the first goal turn. |
| `/goal pause` | Pauses the goal. The loop stops after the running turn. |
| `/goal resume` | Continues a paused goal. The turn count starts at 0 again. `continue` and `unpause` do the same. |
| `/goal clear` | Ends the goal. `stop` and `done` do the same. |
| `/goal wait <pid> [reason]` | Parks an active goal until the process with this pid ends. |
| `/goal unwait` | Removes the wait. An active goal continues at once. |
| `/goal gate list`, `/goal gate add <command>`, `/goal gate remove <n>`, `/goal gate clear` | Shows or changes the gates. `rm <n>` is the same as `remove <n>`. |
| `/subgoal <text>` | Adds a criterion to the goal. |
| `/subgoal` | Lists the criteria. |
| `/subgoal remove <n>`, `/subgoal clear` | Removes one criterion, or all of them. |
| `/todo` | Shows the task list of the session. |

`status`, `show`, `pause`, `resume`, `continue`, `unpause`, and `unwait` ignore more words.
Then the reply adds a note. `clear`, `stop`, and `done` with more words set a new goal, for
example `/goal clear the old logs`.

The status line shows the state, the turns, and the goal:

```text
⊙ Goal (active, 3/20 turns, 1 subgoal, contract): Make the cron tests pass 20 times in a row
⏸ Goal (paused, 5/20 turns — stopped by /stop): Make the cron tests pass 20 times in a row
```

On Telegram, the status reply has **Pause** and **Clear** buttons for an active goal. It has
**Resume** and **Clear** buttons for a paused goal. The CLI gives the commands to send.

## The contract

A goal can have a completion contract. The judge uses the contract to decide if the goal is
done. The contract has five fields: Outcome, Verification, Constraints, Boundaries, and "Stop
when blocked".

Write a field as a line of the goal text, with a prefix and a colon:

```text
/goal Make the cron tests pass 20 times in a row
verify: go test ./internal/cron/ -count=20
must not: change the public API
scope: internal/cron/
```

| Field | Prefixes |
|---|---|
| Outcome | `outcome`, `goal`, `done`, `done when` |
| Verification | `verification`, `verify`, `verified by`, `evidence`, `proof` |
| Constraints | `constraints`, `constraint`, `preserve`, `must not`, `do not change` |
| Boundaries | `boundaries`, `boundary`, `scope`, `allowed`, `files` |
| Stop when blocked | `stop when`, `stop_when`, `blocked`, `stop if blocked`, `give up when` |

The other lines are the goal text. A line such as `Fix bug: the cron test` stays in the goal
text, because `fix bug` is not a prefix.

`/goal draft <text>` asks the judge model to write the contract for you. The bot replies
"Drafting completion contract…" at once. Then it sends the contract, and the first goal turn
starts. When the model cannot write a contract, the goal runs with no contract. Use
`/goal show` to see the contract.

A subgoal is an extra criterion. `/subgoal <text>` adds one, and the judge checks it with the
contract. A subgoal does not start a turn.

## The gates

A gate is a shell command that must pass before the judge runs. Add one with
`/goal gate add <command>`:

```text
/goal gate add go test ./internal/cron/ -count=20
```

The bot replies:

```text
⚿ Gate added: $ go test ./internal/cron/ -count=20 (3 retries, 300s timeout). It must pass before the goal can complete.
```

- Tether Agent checks the gate one time, when you add it. It refuses a command of the
  hardline list.
- On Telegram, it also refuses a command that stops the gateway.
- It refuses a `sudo -S` command when `SUDO_PASSWORD` is not set. When `SUDO_PASSWORD` is
  set, it does not refuse such a command. The command then gets the same check as each other
  command: it needs a confirmation only when it matches a dangerous pattern, for example
  "sudo with privilege flag". Else Tether Agent adds the gate at once.
- A dangerous command needs a confirmation: **Add gate** and **Cancel** on Telegram, `[y/N]`
  in the CLI.
- The gate then runs before each judge call, with no approval prompt.
- It runs in the folder where Tether Agent started, with no secrets in its environment.
  Write `cd /path && <command>` for another folder.
- The timeout is 300 seconds. A timeout counts as a failure.
- When a gate fails, the judge does not run. The next turn gets the output of the gate.
- When a gate fails a fourth time before it passes, the goal pauses.

See [Security](Security) for the limits of the gates.

## Wait for a process

`/goal wait <pid> [reason]` parks an active goal until the process ends. The pid must run on
this host. A parked goal uses no turn and no judge call. The judge can also park the goal, for
example on a background command or for some seconds.

- A wait on a process ends when the process ends, or after 30 minutes.
- A wait for some seconds ends when the time ends. When `delegate_task` batches run, it also
  ends when one batch returns.
- `/goal unwait` removes the wait at once.
- When the wait ends, the next goal turn starts. It shows "▶ Goal barrier lifted — resuming."
- When you send a message to a parked goal, the answer comes as usual. Then a message tells
  you that the goal still waits.

## The judge

The judge reads the goal, the contract, the subgoals, the last answer, the background
processes, the running delegations, and the open items of the task list. It gives one
verdict:

| Verdict | Result |
|---|---|
| `continue` | The next goal turn starts. Its first line is "↻ Goal n/max: reason". |
| `done` | The goal ends: "✓ Goal achieved: reason". |
| `blocked` | The goal pauses: "🚫 Goal judged unachievable — paused: reason". |
| `wait` | The goal parks. See [Wait for a process](#wait-for-a-process). |

- The judge uses the chain of the session, or `goals.judge_provider` when you set it. See
  [Providers](Providers).
- The judge always sends the reasoning effort `low`.
- A judge reply that is not valid counts as `continue`. After 3 such replies in a row, the
  goal pauses. After 5 failed judge calls in a row, the goal pauses.
- A `wait` on a pid that does not run counts as `continue`.
- `/usage` counts the cost of each judge call. `/usage session` also counts the judge calls of
  the session.

## The budget

Each turn that ends with an answer while the goal is active uses one turn of the budget. This
includes the turns of your own messages. A goal has `goals.max_turns` turns (default 20).
After the last turn, the goal pauses:

```text
⏸ Goal paused — 20/20 turns used. Use /goal resume to keep going, or /goal clear to stop.
```

`/goal resume` gives the goal a new budget.

When a turn fails or gives no answer, it uses no turn. The next goal turn starts after 60
seconds. The status line is "↻ Goal n/max: retry after a failed turn." A second failure in a
row pauses the goal.

## The notices

- A `continue` verdict sends no message. Its reason is the first line of the status message
  of the next turn. See [Telegram](Telegram#typing-and-tool-status-lines).
- These events each send a separate message: done, blocked, the budget, the judge failures, a
  gate that fails, a parked goal, and a failed turn.
- Each message about a pause has **Resume** and **Clear** buttons. **Resume** starts the next
  goal turn, the same as `/goal resume`.

## Stop a goal

- `/stop` and `/stop keep` stop the running turn and pause the goal. They also pause a goal
  when no turn runs. The reply adds "⏸ Goal paused. Use /goal resume to continue."
- `/now` runs your waiting message now. The goal stays active.
- `/new`, `/reset`, `/resume`, `/fork`, and `/archive` pause the goal of the current session.
- `/delete` deletes the goal with the session.

## While a goal runs

The session stays busy from one goal turn to the next. A plain text goes into the running
turn as a steer, and other messages wait in the queue. Your messages run before the next goal
turn.

A command that does not work during a task gets the busy reply with the line "A goal runs.
Send /goal pause first." The `/goal` control commands, `/subgoal`, and `/todo` work during a
goal. A new goal text and `/goal draft` do not. They get the reply "Agent is running — use
/goal status / pause / clear / wait mid-run, or /stop before setting a new goal."

## After a restart

When the gateway starts again, it continues each active goal:

- When the gateway was down for less than `agent.resume_freshness_s` (default 3600 seconds),
  the next goal turn starts.
- Else the goal pauses, and the chat gets a message with **Resume** and **Clear** buttons, for
  example "⏸ Goal paused — the gateway was down for 2 h. Use /goal resume to keep going, or
  /goal clear to stop."
- A parked goal keeps its wait.
- When the chat uses another session now, the goal pauses.
- When a turn stopped the gateway 3 times, the goal pauses.
- The gateway does not continue a goal of the CLI.

## In the CLI

`tether chat` runs the goal turns one after the other, with no input between them.

- Before each goal turn, the CLI shows one line, for example
  "↻ Continuing toward goal (2/20): the fix is not verified with a test run."
- Ctrl-C stops the running turn and pauses the goal.
- `/exit`, `/quit`, Ctrl-D, and Ctrl-C at the prompt pause an active goal.
- Send `/goal resume` in a later `tether chat` to continue the goal.
- When the CLI stops with no pause, for example after a crash, the next `tether chat` pauses
  the goal and shows its status line.
- A parked goal continues while the CLI waits at the prompt.
- `/stop` and `/stop keep` at the prompt pause a parked goal. The CLI prints "⏸ Goal paused.
  Use /goal resume to continue."
- The CLI shows no buttons. Its replies give the commands to send.
- All `tether chat` processes of one OS user share one active session. Each `tether chat`
  pauses an active goal when it starts and when it ends. Thus a second `tether chat` pauses the
  goal of the first chat. Run one `tether chat` at a time for a goal, or send `/goal resume` in
  the first chat.

## Configuration

| Key | Default | Effect |
|---|---|---|
| `goals.max_turns` | `20` | The turn budget of a new goal. |
| `goals.judge_provider` | `""` | The provider entry of the judge and of `/goal draft`. `""` uses the chain of the session. |
| `goals.judge_timeout_s` | `30` | The timeout of one judge call or draft call, in seconds. |

## The task list

The agent keeps a task list with the `todo_list` tool, in the `todo` toolset. It uses the
list for work with 3 or more steps. Each item has a status: pending, in progress, completed,
or cancelled.

- On Telegram, the plan shows at the top of the status message, with at most 8 items.
- In the CLI, a plan row shows after each `todo_list` call.
- Send `/todo` to see the full list. It makes no model call:

  ```text
  📋 Plan 2/3 · revision 5
  [x] Find the flaky test
  [x] Replace the real clock with a fake clock
  [>] Run the cron tests 20 times
  ```

- The list stays after a compaction and after a restart.
- Each session has one list. A `delegate_task` child has its own list.

## See also

- [Commands](Commands): all slash commands.
- [Telegram](Telegram): the status message and the buttons.
- [Security](Security): the limits of the gates.
