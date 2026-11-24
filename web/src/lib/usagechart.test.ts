import { describe, it, expect } from "vitest"
import { bucketKeys, bucketOf, formatTick, grainFor, labelStep, niceMax } from "./usagechart"

describe("grainFor", () => {
  it("groups only the range all, by week above 60 days and by month above 365 days", () => {
    expect(grainFor("all", "2026-01-01", "2026-03-02")).toBe("day") // 60 days
    expect(grainFor("all", "2026-01-01", "2026-03-03")).toBe("week") // 61 days
    expect(grainFor("all", "2026-09-01", "2026-11-09")).toBe("week")
    expect(grainFor("all", "2025-01-01", "2026-01-01")).toBe("week") // 365 days
    expect(grainFor("all", "2025-01-01", "2026-01-02")).toBe("month") // 366 days
    expect(grainFor("30d", "2025-01-01", "2026-11-20")).toBe("day")
    expect(grainFor("all", "", "")).toBe("day")
  })
})

describe("bucketOf", () => {
  it("gives the day, the Monday, or the month of a day key", () => {
    expect(bucketOf("2026-11-04", "day")).toBe("2026-11-04")
    expect(bucketOf("2026-11-04", "week")).toBe("2026-11-02")
    expect(bucketOf("2026-11-02", "week")).toBe("2026-11-02")
    expect(bucketOf("2026-11-01", "week")).toBe("2026-10-26")
    expect(bucketOf("2025-01-05", "week")).toBe("2024-12-30")
    expect(bucketOf("2026-11-04", "month")).toBe("2026-11")
  })
})

describe("bucketKeys", () => {
  it("fills each day from the first to the last key", () => {
    expect(bucketKeys("2026-11-17", "2026-11-21", "day")).toEqual([
      "2026-11-17",
      "2026-11-18",
      "2026-11-19",
      "2026-11-20",
      "2026-11-21",
    ])
    expect(bucketKeys("2026-11-20", "2026-11-20", "day")).toEqual(["2026-11-20"])
    expect(bucketKeys("", "2026-11-20", "day")).toEqual([])
  })

  it("gives no duplicate key on the day when DST ends west of UTC", () => {
    const old = process.env.TZ
    process.env.TZ = "America/Los_Angeles"
    try {
      expect(bucketKeys("2026-10-31", "2026-11-03", "day")).toEqual([
        "2026-10-31",
        "2026-11-01",
        "2026-11-02",
        "2026-11-03",
      ])
    } finally {
      if (old === undefined) delete process.env.TZ
      else process.env.TZ = old
    }
  })

  it("fills the weeks with Monday keys and the months across a year", () => {
    const weeks = bucketKeys("2026-09-01", "2026-11-09", "week")
    expect(weeks).toHaveLength(11)
    expect(weeks[0]).toBe("2026-08-31")
    expect(weeks[1]).toBe("2026-09-07")
    expect(weeks[10]).toBe("2026-11-09")
    const months = bucketKeys("2025-01-05", "2026-02-01", "month")
    expect(months).toHaveLength(14)
    expect(months.slice(10)).toEqual(["2025-11", "2025-12", "2026-01", "2026-02"])
  })
})

describe("niceMax", () => {
  it("gives the next 1, 2, or 5 times a power of ten", () => {
    expect(niceMax(0)).toBe(1)
    expect(niceMax(-1)).toBe(1)
    expect(niceMax(0.75)).toBe(1)
    expect(niceMax(1)).toBe(1)
    expect(niceMax(1.2)).toBe(2)
    expect(niceMax(3)).toBe(5)
    expect(niceMax(5)).toBe(5)
    expect(niceMax(7)).toBe(10)
    expect(niceMax(12.4)).toBe(20)
    expect(niceMax(450)).toBe(500)
    expect(niceMax(1000)).toBe(1000)
    expect(niceMax(0.0013)).toBe(0.002)
    expect(niceMax(0.004)).toBe(0.005)
  })
})

describe("formatTick", () => {
  it("gives the decimals that the value needs", () => {
    expect(formatTick(0)).toBe("$0")
    expect(formatTick(1)).toBe("$1")
    expect(formatTick(10)).toBe("$10")
    expect(formatTick(250)).toBe("$250")
    expect(formatTick(0.5)).toBe("$0.50")
    expect(formatTick(2.5)).toBe("$2.50")
    expect(formatTick(0.05)).toBe("$0.05")
    expect(formatTick(0.002)).toBe("$0.002")
    expect(formatTick(0.001)).toBe("$0.001")
    expect(formatTick(0.0025)).toBe("$0.0025")
  })
})

describe("labelStep", () => {
  it("keeps about 56 px for each label", () => {
    expect(labelStep(31, 600)).toBe(3)
    expect(labelStep(8, 600)).toBe(1)
    expect(labelStep(31, 300)).toBe(6)
    expect(labelStep(400, 600)).toBe(38)
    expect(labelStep(0, 600)).toBe(1)
  })
})
