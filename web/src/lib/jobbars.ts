import type { Component } from "vue"
import {
  CheckIcon,
  CircleCheckIcon,
  EyeIcon,
  HourglassIcon,
  PauseIcon,
  TriangleAlertIcon,
} from "@lucide/vue"
import type { JobRow, JobState, RunRow } from "../api/types.gen"

import type { GlyphTone } from "./glyphs"

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

// Gives the icon, the tone, and the word of each job state. Telegram shows the same states as emoji.
export const stateIcon: Record<JobState, { icon: Component; tone: GlyphTone; word: string }> = {
  done: { icon: CheckIcon, tone: "ok", word: "done" },
  paused: { icon: PauseIcon, tone: "muted", word: "paused" },
  failing: { icon: TriangleAlertIcon, tone: "warn", word: "failing" },
  watch: { icon: EyeIcon, tone: "info", word: "watch" },
  ok: { icon: CircleCheckIcon, tone: "ok", word: "ok" },
  pending: { icon: HourglassIcon, tone: "muted", word: "pending" },
}

// Returns the 7-day cost of a job. A dash means that no cost is known. A "+" means that some
// calls have no price, so the true cost is higher.
export function costText(j: JobRow): string {
  if (!j.cost_7d_known) return "—"
  return "$" + j.cost_7d_usd.toFixed(4) + (j.cost_7d_unpriced ? "+" : "")
}
