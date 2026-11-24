import { describe, expect, it } from "vitest"
import { ICON } from "./icons"

describe("ICON", () => {
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
      "start",
      "ops",
      "waiting",
      "theme",
    ]) {
      expect(ICON[k], k).toMatch(/^M/)
    }
  })
})
