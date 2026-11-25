import type { SkillRow } from "../api/types.gen"

export type Tone = "ok" | "warn" | "bad" | "info" | "plain"

// The status token of each tone. "plain" keeps the outline badge as it comes.
export const TONE: Record<Tone, string> = {
  ok: "border-success/40 text-success",
  warn: "border-warning/40 text-warning",
  bad: "border-destructive/40 text-destructive",
  info: "border-info/40 text-info",
  plain: "",
}

// Returns the status chips of one skill as { text, tone } pairs.
export function chips(r: SkillRow): { text: string; tone: Tone }[] {
  const out: { text: string; tone: Tone }[] = []
  if (r.update || r.check.includes("update available"))
    out.push({ text: "update available", tone: "info" })
  if (r.pinned || r.check.includes("pinned")) out.push({ text: "pinned", tone: "plain" })
  if (r.check.includes("modified locally")) out.push({ text: "modified locally", tone: "warn" })
  if (r.check.includes("cannot check")) out.push({ text: "cannot check", tone: "plain" })
  if (r.check.includes("source changed")) out.push({ text: "source changed", tone: "warn" })
  return out
}
