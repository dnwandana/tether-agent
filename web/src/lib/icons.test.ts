import { describe, expect, it } from "vitest"
import { ICON } from "./icons"

describe("ICON", () => {
  it("has the T-hook path of the brand mark", () => {
    expect(ICON.mark).toBe("M6.24 7.2H17.76M12 7.2v6.72c0 3.84 3.84 4.56 5.28 1.92")
  })

  it("has a path for each page and for the new keys", () => {
    for (const k of [
      "chat",
      "sessions",
      "jobs",
      "goals",
      "security",
      "skills",
      "memory",
      "usage",
      "models",
      "system",
      "access",
      "bell",
      "monitor",
      "start",
      "ops",
      "waiting",
      "theme",
    ]) {
      expect(ICON[k], k).toMatch(/^M/)
    }
  })
})
