# Running as a Service

This page shows how to keep Tether Agent on all the time, and how to update it, back it up,
and restore it. It applies mostly to Linux with systemd, for example a small VPS.

## The gateway

Tether Agent has two roles in one binary:

| Role | Command | What it runs |
|---|---|---|
| Chat | `tether chat` | One chat in the terminal. It stops when you quit. |
| Gateway | `tether gateway` | The Telegram channel and the cron scheduler. It runs until you stop it. |

Only the gateway polls Telegram, runs cron jobs, and runs background `delegate_task` batches.
A `tether chat` can run next to the gateway. Both use the same `state.db`.

Only one gateway can run for each data dir. The gateway holds a lock on `gateway.lock` and
writes its process id into the file. A second gateway stops with this error:

```text
another gateway is running (pid N); stop it first or use tether chat
```

The operating system releases the lock when the process ends, so a crash does not leave a
stale lock.

If Telegram is not set up, the gateway runs only cron. Cron jobs then cannot send their
results to a chat.

## Set up the systemd unit

The setup wizard writes the unit file for you. Run `tether setup` and answer yes at
`Write the systemd unit?`. The wizard keeps your other settings. If a unit file exists, the
wizard copies it to a `.bak` file first.

There are two kinds of unit:

| Kind | Command | Unit file | When to use |
|---|---|---|---|
| User unit | `tether setup` | `~/.config/systemd/user/tether.service` | The normal case. No root is necessary. |
| System unit | `sudo tether setup --system` | `/etc/systemd/system/tether.service` | You want a system service. The unit gets a `User=` line with your user name. |

With `--system` under `sudo`, the wizard uses the data dir of the user who called `sudo`, for
example `/home/you/.tether`. A `--data-dir` flag or `TETHER_HOME` wins over this rule. After
the wizard, that user owns the data dir and its files, because the system unit runs as that
user. If the change of the owner fails, the wizard prints the command to run, for example:

```text
setup: cannot give /home/you/.tether to you: <cause>. Run: sudo chown -R you: /home/you/.tether
```

The unit runs `<binary> gateway --data-dir <dir> --log-stderr`. `<binary>` is the path of the
`tether` binary that ran the wizard. The unit also contains these settings:

| Line | Purpose |
|---|---|
| `ExecReload=/bin/kill -USR1 $MAINPID` | `systemctl reload tether` restarts the gateway after the running turns. |
| `Restart=on-failure`, `RestartSec=5` | systemd starts the gateway again 5 seconds after a crash. |
| `RestartForceExitStatus=75`, `SuccessExitStatus=75` | A `/restart` ends with exit code 75. systemd starts the gateway again and does not log a failure. |
| `TimeoutStopSec=<n>` | The larger of 60 and `agent.restart_drain_timeout_s`, plus 30 seconds. The default is 210. |
| `KillMode=mixed` | systemd sends `SIGTERM` only to the gateway, so the tool commands can finish during the drain. |
| `Environment=TZ=<zone>` | The time zone of the cron schedules. |

`docs/tether.service` in the repository is an example of this unit.

After the wizard writes the unit, it shows the path and the commands that start the service.

> **Note:** the wizard only writes the file. It does not run `systemctl`.

> **Caution:** keep the API keys of the service in `~/.tether/.env`. The service does not get
> the variables of your shell. `tether doctor` in your shell can pass with a key from the
> shell, but the service then has no key.

### Start a user unit

1. Load the new unit:

   ```sh
   systemctl --user daemon-reload
   ```

2. Start the gateway now and at each boot:

   ```sh
   systemctl --user enable --now tether
   ```

3. Keep the user services on after you log out:

   ```sh
   loginctl enable-linger $USER
   ```

4. Look at the log:

   ```sh
   journalctl --user -u tether -f
   ```

For a system unit, use the same commands without `--user`, with `sudo`, and without the
`loginctl` step.

### The time zone of cron jobs

Cron schedules use the time zone of the gateway process. The wizard writes it into the unit
as `Environment=TZ=<zone>`. The wizard finds the zone in this order:

1. The `TZ` environment variable.
2. `/etc/timezone`.
3. The target of the `/etc/localtime` link.
4. `UTC`.

To change the zone, edit the `Environment=TZ=` line, then run `systemctl --user daemon-reload`
and `systemctl --user restart tether`.

### A unit from an older release

The stop and restart settings of the unit changed in v0.7.0. If you used an older release,
run `tether setup` again and answer yes at `Write the systemd unit?`. `tether doctor` and the
gateway log give a warning when the unit is old.

## Stop and restart

When the gateway stops, it tries to finish the running replies first. It does these steps:

1. It stops reading new messages.
2. It sends a notice to each chat with a running reply, for example:
   `⚠️ The gateway is restarting. I will try to finish this reply first, for up to 3 min.`
3. It waits for the running turns to end, and for the messages that it still prepares, for
   example a voice note in speech to text. It waits for at most
   `agent.restart_drain_timeout_s` seconds. The default is 180.
4. It saves the messages that still wait in the queue, and it cancels the rest of the work.
   A message that it could not prepare in time, or that comes after the drain, gets this
   reply: `⚠️ Tether Agent stopped before it could read this message. Send it again.`

On the next start, the gateway continues each reply that it cut, if the cut is recent
(`agent.resume_freshness_s`, default 3600 seconds; `0` turns this off). It never runs an old
tool call again. Photos and files in a waiting message are lost, and the agent gets a note
about the loss. After 3 resumes in a row, the gateway stops the automatic resume and tells
you.

An active goal continues when the gateway was down for less than `agent.resume_freshness_s`.
Else it pauses, and the chat gets a notice with Resume and Clear buttons.

There are three ways to restart the gateway:

| Method | Command |
|---|---|
| From a chat | Send `/restart`. The gateway restarts after the running turns end. |
| With systemd | `systemctl --user reload tether` (sends `SIGUSR1`), or `systemctl --user restart tether`. |
| With a signal | `kill -USR1 <pid>` |

Under systemd, a restart from `/restart` or `SIGUSR1` ends the process with exit code 75, and
systemd starts it again. Without systemd, the process starts itself again in place.

After a `/restart`, the chat that sent it gets a message that the session continues. After an
update to a new version, the home chat gets a message with the old and the new version.

`SIGINT` and `SIGTERM` stop the gateway with the same drain steps.

A coding task of `acp_delegate` often takes longer than the drain. The reply to `/restart` names
each running coding task, for example:

`A coding task of claude-code runs. It stops if it does not end in 3 min.`

After the drain, the gateway cancels the task and stops the coding agent. The files that the
coding agent changed stay. The next `acp_delegate` call in the same session continues the
context of the coding agent.

## Update

`tether update` replaces the binary with the latest release.

```sh
tether update            # install the latest release
tether update --check    # only show if a newer release exists
tether update --tag v1.2.3   # install a specific release
```

The command does these steps:

1. It gets the release data from GitHub.
2. It downloads `checksums.txt` and the archive for your platform.
3. It checks the SHA-256 sum of the archive.
4. It extracts the new binary and runs `<new binary> version` as a test.
5. It replaces the old binary in one atomic rename. It follows a symlink to the real file.

The command checks that it can write the binary before the download. A binary in a system
folder, for example `/usr/local/bin`, needs `sudo tether update`. With `sudo`, the command
finds the gateway of the user who ran `sudo`. It also reads the `update` keys from the
`config.yaml` of that user. When the file does not load, the command prints a warning and uses
the default time limits.

When a gateway runs, the new binary is not active until the gateway restarts:

- In a terminal, the command asks
  `Restart the gateway now? The running turns get time to end first.` If you answer yes, it
  runs `systemctl --user restart tether` for a user unit.
- For a system unit, it runs `systemctl restart tether` only when it runs as root
  (`sudo tether update`). Else it prints `sudo systemctl restart tether`.
- Under `sudo` with a user unit, it does not ask. It prints the restart command for your user.
- With no unit file, it tells you to stop the gateway and start it again.
- Without a terminal, it prints the restart command.
- `--restart` and `--no-restart` give the answer before the question.

The restart after an update uses the same drain. A running coding task stops when the drain
ends. Wait for the task to end before you restart, if you want its result.

> **Note:** if the agent runs `tether update` from inside the gateway, the command cannot
> restart the gateway. Send `/restart` in the chat after the update.

On macOS there is no service step. Stop the gateway and start it again after the update.

## Back up

`tether backup` writes one `.tar.gz` archive. It can run while the gateway runs.

```sh
tether backup                   # writes ~/.tether/backups/tether-backup-<date>-<time>.tar.gz
tether backup --out /path/file.tar.gz
tether backup --out - | ssh nas 'cat > tether.tar.gz'
```

The archive contains these items:

- `state.db` (a consistent copy, made with SQLite `VACUUM INTO`)
- `config.yaml`
- `memory/memory.md`, `memory/user_profile.md`, and the memory notes in `memory/notes/`
- `skills/`
- `scripts/`

The archive never contains `.env`. A literal secret in `config.yaml` becomes a `${NAME}`
reference, and the known secret values are masked in the database copy and in the text files.
The archive still holds your messages, so keep it private.

To make a backup each night, add this line to your crontab (`crontab -e`). Cron does not read
your shell `PATH`, so give the full path of the binary:

```sh
0 3 * * * $HOME/.local/bin/tether backup
```

Each backup is a new file. Delete the old archives in `backups/` from time to time.

## Restore

Stop the gateway and quit every `tether chat` before a restore. The restore refuses to run
while a gateway holds the lock or a chat has the database open.

```sh
systemctl --user stop tether
tether restore ~/.tether/backups/tether-backup-<date>-<time>.tar.gz
```

The restore does these steps:

1. It checks the full archive before it changes anything. It refuses a file that is not in the
   allowed list, and it refuses an archive from a newer database schema.
2. It asks you to type `restore` to continue. `--yes` skips the question. A restore from stdin
   (`-`) or without a terminal needs `--yes`.
3. It moves the current data to `pre-restore-<time>/` in the data dir.
4. It puts the data from the archive in place.
5. It lists the variables that `.env` must contain. The list has each `${NAME}` in
   `config.yaml`. It also has the key variable of each preset entry with no `api_key`, for
   example `OPENROUTER_API_KEY`. If a variable is missing, the list starts with
   `config.yaml needs these variables in .env:`.

### Move to a new host

1. Run `tether backup` on the old host.
2. Copy the archive and `~/.tether/.env` to the new host.
3. Install the binary on the new host. Refer to [Getting Started](Getting-Started).
4. Run `tether restore FILE` on the new host.
5. Run `tether setup` and answer yes at `Write the systemd unit?`. At the other questions,
   press Enter to keep the restored values.
6. Start the service: `systemctl --user enable --now tether`.

## Logs

`tether chat` and `tether gateway` write JSON lines to `logs/tether.log` in the data dir. The
log masks secrets. The file rotates at `log.max_size_mb` (default 10) and keeps
`log.max_files` (default 3) old files.

`--log-stderr` sends the log to stderr instead of the file. The systemd unit uses this flag,
so the log of the service is in the journal (`journalctl --user -u tether`), not in
`logs/tether.log`. To see more detail for one run, add `--log-level debug`.
