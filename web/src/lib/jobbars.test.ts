import { describe, it, expect } from "vitest"
import type { JobRow, RunRow } from "../api/types.gen"
import { costText, lastSeven, stateGlyph } from "./jobbars"

const runs = (...s: string[]) => s.map((status) => ({ status }) as RunRow)
const j = (o: object) =>
  ({ cost_7d_usd: 0.0102, cost_7d_known: true, cost_7d_unpriced: false, ...o }) as JobRow

describe("lastSeven", () => {
  it("pads at the start and puts the newest run last", () => {
    // Newest first in, oldest first out.
    expect(lastSeven(runs("ok", "error", "ok"))).toEqual([
      "none",
      "none",
      "none",
      "none",
      "ok",
      "bad",
      "ok",
    ])
  })

  it("shows a stopped run as stop and each other status that is not ok as bad", () => {
    expect(lastSeven(runs("stopped", "skipped", "timeout", "error", "ok", "ok", "ok"))).toEqual([
      "ok",
      "ok",
      "ok",
      "bad",
      "bad",
      "bad",
      "stop",
    ])
  })

  it("maps each job state to its Telegram glyph", () => {
    expect(stateGlyph).toEqual({
      done: "✔",
      paused: "⏸",
      failing: "⚠",
      watch: "👁",
      ok: "✅",
      pending: "⏳",
    })
  })

  it("gives the 7-day cost text", () => {
    expect(costText(j({}))).toBe("$0.0102")
    expect(costText(j({ cost_7d_unpriced: true }))).toBe("$0.0102+")
    expect(costText(j({ cost_7d_known: false, cost_7d_usd: 0 }))).toBe("—")
  })

  it("keeps only the newest 7 runs", () => {
    expect(lastSeven(runs("error", "ok", "ok", "ok", "ok", "ok", "ok", "ok", "ok"))).toEqual([
      "ok",
      "ok",
      "ok",
      "ok",
      "ok",
      "ok",
      "bad",
    ])
  })

  it("gives 7 empty bars for no runs", () => {
    expect(lastSeven([])).toEqual(Array(7).fill("none"))
  })
})
