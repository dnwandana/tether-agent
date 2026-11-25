import { describe, it, expect } from "vitest"
import type { SkillRow } from "../api/types.gen"
import { TONE, chips } from "./skills"

const skill = (o: Partial<SkillRow>): SkillRow => ({
  name: "x",
  description: "",
  trust: "community",
  source: "",
  commit: "",
  pinned: false,
  verdict: "",
  required_env: [],
  check: "",
  update: false,
  actions: [],
  ...o,
})

describe("chips", () => {
  it("gives no chip for a skill with no check value", () => {
    expect(chips(skill({}))).toEqual([])
  })

  it("gives one chip for each check value", () => {
    expect(chips(skill({ update: true }))).toEqual([{ text: "update available", tone: "info" }])
    expect(chips(skill({ check: "update available" }))).toEqual([
      { text: "update available", tone: "info" },
    ])
    expect(chips(skill({ pinned: true }))).toEqual([{ text: "pinned", tone: "plain" }])
    expect(chips(skill({ check: "pinned" }))).toEqual([{ text: "pinned", tone: "plain" }])
    expect(chips(skill({ check: "modified locally" }))).toEqual([
      { text: "modified locally", tone: "warn" },
    ])
    expect(chips(skill({ check: "cannot check" }))).toEqual([
      { text: "cannot check", tone: "plain" },
    ])
    expect(chips(skill({ check: "source changed" }))).toEqual([
      { text: "source changed", tone: "warn" },
    ])
  })

  it("keeps a fixed order for many check values", () => {
    expect(
      chips(skill({ pinned: true, check: "source changed, modified locally, update available" })),
    ).toEqual([
      { text: "update available", tone: "info" },
      { text: "pinned", tone: "plain" },
      { text: "modified locally", tone: "warn" },
      { text: "source changed", tone: "warn" },
    ])
  })
})

describe("TONE", () => {
  it("uses only the status tokens", () => {
    expect(TONE).toEqual({
      ok: "border-success/40 text-success",
      warn: "border-warning/40 text-warning",
      bad: "border-destructive/40 text-destructive",
      info: "border-info/40 text-info",
      plain: "",
    })
  })
})
