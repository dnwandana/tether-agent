import { describe, it, expect } from "vitest"
import type { RunRow } from "../api/types.gen"
import { lastSeven } from "./jobbars"

const runs = (...s: string[]) => s.map((status) => ({ status }) as RunRow)

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

  it("shows each status that is not ok as bad", () => {
    expect(lastSeven(runs("stopped", "skipped", "timeout", "error", "ok", "ok", "ok"))).toEqual([
      "ok",
      "ok",
      "ok",
      "bad",
      "bad",
      "bad",
      "bad",
    ])
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
