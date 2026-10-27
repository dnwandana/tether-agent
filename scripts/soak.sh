#!/usr/bin/env bash
# soak.sh runs "tether gateway" against local fake Telegram and provider
# servers (scripts/soakfake) for the given number of minutes. Telegram and cron
# are on. The browser and MCP are off, so the run measures the idle footprint.
#
# Usage: scripts/soak.sh [minutes]   (default: 60)
#
# Environment:
#   SOAK_INTERVAL   seconds between two samples (default: 30)
#   SOAK_MSG_EVERY  time between two fake user messages, as a Go duration
#                   (default: 60s; 0 sends none)
#   SOAK_MAX_RSS_KB the RSS limit in KiB (default: 40960, that is 40 MB)
#   SOAK_MAX_GOROUTINE_GROWTH the goroutine growth limit (default: 10)
#   SOAK_KEEP=1     keep the work folder after the run
#
# The script fails when a sample has an RSS above the limit, when the last
# goroutine count is more than the limit above the first one, or when the
# gateway stops before the end.
set -euo pipefail

minutes="${1:-60}"
interval="${SOAK_INTERVAL:-30}"
msg_every="${SOAK_MSG_EVERY:-60s}"
max_rss_kb="${SOAK_MAX_RSS_KB:-40960}"
max_growth="${SOAK_MAX_GOROUTINE_GROWTH:-10}"

case "$minutes" in
'' | *[!0-9]*)
	echo "soak: the minutes argument must be a whole number, not \"$minutes\"" >&2
	exit 2
	;;
esac
for tool in go curl ps; do
	if ! command -v "$tool" >/dev/null 2>&1; then
		echo "soak: the $tool command is not in PATH. Install it and run the soak again." >&2
		exit 2
	fi
done

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/tether-soak.XXXXXX")"
fake_pid=""
gw_pid=""

cleanup() {
	if [ -n "$gw_pid" ] && kill -0 "$gw_pid" 2>/dev/null; then
		kill -TERM "$gw_pid" 2>/dev/null || true
		wait "$gw_pid" 2>/dev/null || true
	fi
	if [ -n "$fake_pid" ] && kill -0 "$fake_pid" 2>/dev/null; then
		kill -TERM "$fake_pid" 2>/dev/null || true
		wait "$fake_pid" 2>/dev/null || true
	fi
	if [ "${SOAK_KEEP:-0}" = "1" ]; then
		echo "soak: the work folder is $work"
	else
		rm -rf "$work"
	fi
}
trap cleanup EXIT

echo "soak: build the fake servers"
(cd "$root" && CGO_ENABLED=0 go build -o "$work/soakfake" ./scripts/soakfake)
"$work/soakfake" -ready "$work/ready.env" -msg-every "$msg_every" 2>"$work/soakfake.log" &
fake_pid=$!
for _ in $(seq 1 100); do
	[ -f "$work/ready.env" ] && break
	sleep 0.1
done
if [ ! -f "$work/ready.env" ]; then
	echo "soak: the fake servers did not start. See $work/soakfake.log." >&2
	SOAK_KEEP=1
	exit 1
fi
# shellcheck disable=SC1091
. "$work/ready.env"

# The Telegram root is a link-time value, so the binary talks only to the fake
# Bot API. The other flags are the release flags.
echo "soak: build tether with the fake Telegram root $TG_URL"
(cd "$root" && CGO_ENABLED=0 go build -trimpath \
	-ldflags "-s -w -X main.telegramBaseURL=$TG_URL" -o "$work/tether" ./cmd/tether)

data="$work/data"
mkdir -p "$data" "$work/home"
cat >"$data/config.yaml" <<EOF
providers:
  primary:
    format: openai
    base_url: $LLM_URL
    api_key: soak-key
    model: soak-model
pricing:
  sources: []
telegram:
  enabled: true
  bot_token: soak-token
  allowed_users: ["111"]
  home_chat: "111"
EOF
chmod 600 "$data/config.yaml"

pprof="${TETHER_PPROF:-$PPROF_ADDR}"
echo "soak: start the gateway (pprof on $pprof)"
env -u TELEGRAM_BOT_TOKEN HOME="$work/home" TETHER_HOME="$data" TETHER_PPROF="$pprof" \
	"$work/tether" gateway --data-dir "$data" 2>"$work/gateway.stderr" &
gw_pid=$!

goroutines() {
	curl -fsS --max-time 5 "http://$pprof/debug/pprof/goroutine?debug=1" 2>/dev/null |
		sed -n 's/^goroutine profile: total \([0-9][0-9]*\).*/\1/p'
}

for _ in $(seq 1 100); do
	[ -n "$(goroutines)" ] && break
	if ! kill -0 "$gw_pid" 2>/dev/null; then break; fi
	sleep 0.1
done
if [ -z "$(goroutines)" ]; then
	echo "soak: FAIL the gateway did not start. Its stderr:" >&2
	cat "$work/gateway.stderr" >&2
	SOAK_KEEP=1
	exit 1
fi
# Let the channel and the scheduler settle before the first sample.
sleep 5

samples=$((minutes * 60 / interval))
fail=0
first_g=""
last_g=""
max_rss=0
printf '%8s %10s %8s %10s %10s\n' "time_s" "rss_kb" "rss_mb" "goroutines" "llm_calls"
start=$(date +%s)
for i in $(seq 0 "$samples"); do
	if [ "$i" -gt 0 ]; then
		sleep "$interval"
	fi
	if ! kill -0 "$gw_pid" 2>/dev/null; then
		echo "soak: FAIL the gateway stopped. Its stderr:" >&2
		cat "$work/gateway.stderr" >&2
		fail=1
		break
	fi
	rss=$(ps -o rss= -p "$gw_pid" | tr -d ' ')
	g=$(goroutines)
	calls=$(curl -fsS --max-time 5 "$TG_URL/stats" 2>/dev/null |
		sed -n 's/.*"llm_calls":\([0-9][0-9]*\).*/\1/p')
	printf '%8s %10s %8s %10s %10s\n' "$(($(date +%s) - start))" "$rss" \
		"$(awk -v k="$rss" 'BEGIN { printf "%.1f", k / 1024 }')" "${g:-?}" "${calls:-0}"
	[ -z "$first_g" ] && first_g="$g"
	last_g="$g"
	if [ "$rss" -gt "$max_rss" ]; then max_rss="$rss"; fi
	if [ "$rss" -gt "$max_rss_kb" ]; then
		echo "soak: FAIL the RSS is $rss KiB, above the limit of $max_rss_kb KiB" >&2
		fail=1
	fi
done

if [ -n "$first_g" ] && [ -n "$last_g" ] && [ $((last_g - first_g)) -gt "$max_growth" ]; then
	echo "soak: FAIL the goroutine count grew from $first_g to $last_g (limit +$max_growth)" >&2
	fail=1
fi

kill -TERM "$gw_pid"
if ! wait "$gw_pid"; then
	echo "soak: FAIL the gateway did not stop cleanly. Its stderr:" >&2
	cat "$work/gateway.stderr" >&2
	fail=1
fi
gw_pid=""
if grep -q '"level":"ERROR"' "$data/logs/tether.log" 2>/dev/null; then
	echo "soak: the gateway log has errors:" >&2
	grep '"level":"ERROR"' "$data/logs/tether.log" | tail -n 5 >&2
	fail=1
fi

echo "soak: max RSS $max_rss KiB, goroutines $first_g -> $last_g, $minutes min"
if [ "$fail" -ne 0 ]; then
	echo "soak: FAIL"
	SOAK_KEEP=1
	exit 1
fi
echo "soak: PASS"
