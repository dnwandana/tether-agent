import type { RunRow } from "../api/types.gen"

// Returns the 7 bars of the newest runs, oldest first. runs is newest first, as the server
// sends it. Missing runs are "none" at the start, and each status that is not "ok" is "bad".
export function lastSeven(runs: RunRow[]): ("ok" | "bad" | "none")[] {
  const out: ("ok" | "bad" | "none")[] = runs
    .slice(0, 7)
    .map((r) => (r.status === "ok" ? "ok" : "bad"))
    .reverse()
  while (out.length < 7) out.unshift("none")
  return out
}
