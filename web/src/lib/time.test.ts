import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { dayLabel, duration, fullTime, shortTime } from "./time"

beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(new Date(2026, 10, 24, 12, 0))
})
afterEach(() => vi.useRealTimers())

const EMPTY = [null, undefined, "", "not a date", "0001-01-01T00:00:00Z", "1999-12-31T12:00:00"]

describe("shortTime", () => {
  it("gives the month, the day, and a 24-hour time", () => {
    const t = new Date(2026, 10, 24, 7, 29)
    expect(shortTime(t)).toBe("Nov 24 07:29")
    expect(shortTime(t.toISOString())).toBe("Nov 24 07:29")
    expect(shortTime(t.getTime())).toBe("Nov 24 07:29")
  })

  it("shows midnight as 00:05, not 24:05", () => {
    expect(shortTime(new Date(2026, 10, 24, 0, 5))).toBe("Nov 24 00:05")
  })

  it("adds the year only for another year", () => {
    expect(shortTime(new Date(2025, 0, 3, 9, 0))).toBe("Jan 3, 2025 09:00")
    expect(shortTime(new Date(2026, 0, 3, 9, 0))).toBe("Jan 3 09:00")
  })

  it("gives an empty text for an empty, an invalid, and a zero Go time", () => {
    for (const v of EMPTY) expect(shortTime(v), String(v)).toBe("")
  })
})

describe("fullTime", () => {
  it("gives the full local date and time", () => {
    expect(fullTime(new Date(2026, 10, 24, 7, 29, 5))).toMatch(/^Tue, Nov 24, 2026, 07:29:05 \S+/)
  })

  it("gives an empty text for an empty, an invalid, and a zero Go time", () => {
    for (const v of EMPTY) expect(fullTime(v), String(v)).toBe("")
  })
})

describe("dayLabel", () => {
  it("builds the label from the parts of the key, also west of UTC", () => {
    const tz = process.env.TZ
    process.env.TZ = "America/Los_Angeles"
    try {
      expect(dayLabel("2026-11-04")).toBe("Nov 4")
      expect(dayLabel("2026-01-31")).toBe("Jan 31")
    } finally {
      if (tz === undefined) delete process.env.TZ
      else process.env.TZ = tz
    }
  })

  it("gives back a key that is not a day key", () => {
    expect(dayLabel("2026-W45")).toBe("2026-W45")
    expect(dayLabel("")).toBe("")
  })
})

describe("duration", () => {
  it("gives seconds, or minutes and seconds", () => {
    expect(duration(0)).toBe("0 s")
    expect(duration(38)).toBe("38 s")
    expect(duration(3.5)).toBe("4 s")
    expect(duration(125)).toBe("2 m 5 s")
  })
})
