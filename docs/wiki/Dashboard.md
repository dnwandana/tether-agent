# Dashboard

The gateway can serve a web page for the owner. The page has the chat, the prompts, and the
controls of Telegram and the CLI. Only the gateway serves it. `tether chat` never serves it.

The page needs Safari 16.4 (iOS 16.4), Chrome 111, Firefox 128, or a later version.

## Turn it on

Add this to `config.yaml`, then restart the gateway:

```yaml
dashboard:
  enabled: true
  listen: 127.0.0.1:8787
```

At start, the gateway writes this notice to stderr and to the log:

```text
⚠ dashboard: on at https://agent.tail1234.ts.net:8787
  A signed-in device can chat, approve commands, and change settings as the owner.
  Sign in: open the address and tap "Request access", or run
  tether dashboard link in a terminal on this host.
```

See [docs/CONFIG.md](https://github.com/dnwandana/tether-agent/blob/main/docs/CONFIG.md) for each
`dashboard.*` key.

## Sign in

There are two ways to sign in.

1. Request access. Open the address and tap "Request access". The page shows a 4-digit check
   code. Compare it with the code on the card in Telegram, or on a signed-in page. Tap Allow.
2. A link from a terminal. Run `tether dashboard link` on the host. Open the link in your
   browser.

The output of `tether dashboard link` looks like this:

```text
$ tether dashboard link
https://agent.tail1234.ts.net:8787/#code=K7QF-2M9X
The code works once and expires in 5 minutes.
```

A session lasts `dashboard.session_hours` hours (default 24). After that, the page shows
`Your session ended. Request access again.`

To sign out:

- Run `tether dashboard devices` to list the signed-in devices.
- Run `tether dashboard signout-all` to end every session. It needs a terminal.
- Use Revoke on the Approvals and security page of the dashboard.

## Reach it from a phone

The dashboard listens on one address. You choose how the phone reaches it.

### An SSH tunnel

Run this on the phone or the laptop:

```sh
ssh -L 8787:127.0.0.1:8787 user@host
```

Then open `http://localhost:8787`. Any local port works, because the loopback names are allowed
on any port. Plain HTTP on `localhost` is a secure context.

### Tailscale

1. Set `dashboard.listen` to the Tailscale address.
2. Put the MagicDNS name in `dashboard.allowed_hosts`.
3. Run `tailscale cert <name>`.
4. Set `dashboard.tls_cert` and `dashboard.tls_key` to the two files.

A renewed certificate loads with no restart.

### A VPN address

Set `dashboard.listen` to the address, for example the `tun0` address. After a reboot, the
address can be down. The listener tries again with a back-off of up to 1 minute.

### A TLS proxy

Use Caddy or nginx. Set `dashboard.public_url` and `dashboard.trusted_proxies`. For nginx, use
this block:

```nginx
location / {
    proxy_pass http://127.0.0.1:8787;
    proxy_http_version 1.1;
    proxy_buffering off;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $remote_addr;
    proxy_set_header X-Forwarded-Proto $scheme;
}
```

The server also sends `X-Accel-Buffering: no` on the event stream.

## HTTPS and the browser

Voice notes, Copy, and notifications need HTTPS or `localhost`. On plain HTTP, the page hides
them and shows a note with a link to the HTTPS steps.

A browser opens at most 6 HTTP/1.1 connections to one host for all tabs. On plain HTTP, keep few
tabs open. Or use a TLS proxy with HTTP/2.

## Pages

| Page | What it does |
|---|---|
| Chat | Talk to the agent, stream the answer, and attach files or voice notes. |
| Waiting for you | A tray with the open cards. The oldest card shows in full. Deny all, or approve all once. On a phone, the ⏳ Waiting tab opens it. |
| Sessions | List the sessions of all channels. Read the transcript. Open, rename, fork, archive, unarchive, or export one. Child sessions fold under their parent. |
| Jobs | Show the brief, the cron jobs, their runs, and the held sends. |
| Goals | Show the goals of all sessions with their state and gates. |
| Approvals and security | Show the mode of each scope, the Always answers, the approval log, and the signed-in devices. |
| Skills | Show the installed skills and the catalog. Install, update, or remove a skill. Read the files of a skill. A dangerous skill installs only from a terminal. |
| Memory | Read, add, edit, and remove memory entries. Search the notes. |
| Usage | Show the cost and the tokens by day and by model. Point at a bar, or tap it, to see the cost of that day. |
| Models | Show the provider entries, their health, and the role table. |
| System | Run the doctor. Show the MCP servers and the running work. |
| System operations | Show the masked config, make and list backups, show the restore steps, update, restart, and read the log. |
| Access | Approve a pairing code. Revoke an approved person. Deny a request. See the revoked people. |
| Getting started | Show each setup step with its next command. |

An answered card leaves a one-line record in the chat. The record lasts until the gateway
restarts. The approval log keeps the permanent record.

The pages update by themselves when the data changes, when the page connects again, and when
you come back to the tab. Each page header shows the time of the last update and ↻ Refresh.

The theme follows the theme of the system by default. The theme menu at the right end of the
status bar sets System, Dark, or Light.

The page shows each time in the time zone of your browser. A job schedule shows in that zone
too; the tooltip shows the cron expression and the zone of the host.

## Alerts

While a tab is open on a desktop, the page shows the count in the tab title as
`(N) Tether Agent Dashboard`. It plays a sound and shows a browser notification when a card
opens. Turn the sound on or off, and allow notifications, from the theme menu in the status bar. A phone browser freezes a hidden page. On a phone, Telegram stays the alert channel.

## Security

- Each request needs a session, also on loopback.
- A human tap approves each new device.
- A signed-in device is the owner.

See [Security](Security#dashboard).

## Troubleshooting

- `dashboard: off. Set dashboard.enabled: true and restart the gateway.` Turn the dashboard on
  in `config.yaml`.
- A port in use or a bad value keeps the dashboard off. The gateway keeps running. See the log
  and `tether doctor`.
- A wildcard listen address needs `dashboard.public_url` or `dashboard.allowed_hosts`.
- `Your session ended. Request access again.` Sign in again.
