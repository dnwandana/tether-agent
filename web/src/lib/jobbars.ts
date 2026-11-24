import type { JobRow, JobState, RunRow } from "../api/types.gen"

export type Bar = "ok" | "bad" | "stop" | "none"

// Returns the 7 bars of the newest runs, oldest first. runs is newest first, as the server
// sends it. Missing runs are "none" at the start. The status "ok" is "ok", the status
// "stopped" is "stop", and each other status is "bad".
export function lastSeven(runs: RunRow[]): Bar[] {
  const out: Bar[] = runs
    .slice(0, 7)
    .map((r): Bar => (r.status === "ok" ? "ok" : r.status === "stopped" ? "stop" : "bad"))
    .reverse()
  while (out.length < 7) out.unshift("none")
  return out
}

// Gives the Telegram glyph of each job state, so the page and the `/jobs` text agree.
export const stateGlyph: Record<JobState, string> = {
  done: "✔",
  paused: "⏸",
  failing: "⚠",
  watch: "👁",
  ok: "✅",
  pending: "⏳",
}

// Returns the 7-day cost of a job. A dash means that no cost is known. A "+" means that some
// calls have no price, so the true cost is higher.
export function costText(j: JobRow): string {
  if (!j.cost_7d_known) return "—"
  return "$" + j.cost_7d_usd.toFixed(4) + (j.cost_7d_unpriced ? "+" : "")
}
