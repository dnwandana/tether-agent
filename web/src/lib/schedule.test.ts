import { describe, it, expect } from "vitest"
import { readableSchedule } from "./schedule"
import { shortTime } from "./time"

const text = (raw: string, host = 0, browser = 0) =>
  readableSchedule(raw, "UTC", host, browser).text

describe("readableSchedule", () => {
  it("moves a daily hour from the host zone to the browser zone", () => {
    expect(readableSchedule("0 1 * * *", "UTC", 0, 420)).toEqual({
      text: "daily 08:00",
      title: "0 1 * * * · host time UTC",
    })
    expect(text("0 1 * * *", 0, 330)).toBe("daily 06:30")
    expect(text("0 20 * * *", 0, 300)).toBe("daily 01:00")
  })

  it("reads the intervals", () => {
    expect(text("every 3h")).toBe("every 3 h")
    expect(text("every 30m")).toBe("every 30 min")
    expect(text("every 2d")).toBe("every 2 d")
  })

  it("reads weekdays and weekly forms, and moves the day across midnight", () => {
    expect(text("0 9 * * 1-5")).toBe("weekdays 09:00")
    expect(text("15 7 * * 0")).toBe("weekly Sun 07:15")
    expect(text("15 7 * * 7")).toBe("weekly Sun 07:15")
    expect(text("30 22 * * 1", 0, 420)).toBe("weekly Tue 05:30")
    expect(text("0 2 * * 0", 60, -300)).toBe("weekly Sat 20:00")
    expect(text("0 23 * * 1-5", 0, 60)).toBe("Tue–Sat 00:00")
  })

  it("shows a one-shot time in the short format", () => {
    expect(text("2026-11-08T17:50:00Z")).toBe("once " + shortTime("2026-11-08T17:50:00Z"))
  })

  it("shows another cron form as raw text in host time", () => {
    expect(text("*/15 * * * *")).toBe("*/15 * * * * (host time)")
    expect(text("0 9 1 * *")).toBe("0 9 1 * * (host time)")
    expect(text("0 9 * * MON")).toBe("0 9 * * MON (host time)")
  })

  it("gives only the raw text as the title when the host zone is not known", () => {
    expect(readableSchedule("every 3h", "", 0, 0).title).toBe("every 3h")
  })

  it("does not move a cron hour when the host zone is not known", () => {
    expect(readableSchedule("0 9 * * *", "", 0, 180).text).toBe("0 9 * * * (host time)")
    expect(readableSchedule("0 9 * * 1-5", "", 0, -300).text).toBe("0 9 * * 1-5 (host time)")
  })
})
