import { nextTick } from "vue"
import { describe, expect, it, vi } from "vitest"
import { applyStatusEvents, status } from "./status"

describe("status store", () => {
  it("starts as connecting, with no offline time", () => {
    expect(status.stream).toBe("connecting")
    expect(status.offlineSince).toBeNull()
  })

  it("keeps the time that the stream went offline until it opens again", async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date(2026, 10, 23, 9, 0, 0))
    status.stream = "reconnecting"
    await nextTick()
    expect(status.offlineSince).toBe(new Date(2026, 10, 23, 9, 0, 0).getTime())
    vi.advanceTimersByTime(5_000)
    status.stream = "connecting"
    await nextTick()
    status.stream = "reconnecting"
    await nextTick()
    expect(status.offlineSince).toBe(new Date(2026, 10, 23, 9, 0, 0).getTime())
    status.stream = "open"
    await nextTick()
    expect(status.offlineSince).toBeNull()
    vi.useRealTimers()
  })

  it("takes the status from hello and merges a status event", () => {
    applyStatusEvents([
      {
        name: "hello",
        data: {
          turn: null,
          cards: [],
          status: {
            model: "glm-4.6",
            entry: "primary",
            fallback: false,
            fallback_cause: "",
            mode: "guarded",
            waiting: 1,
            running: 2,
            spend24h: 1.8421,
            unpriced: false,
            tls: true,
          },
        },
      },
    ])
    expect(status.model).toBe("glm-4.6")
    expect(status.running).toBe(2)
    expect(status.tls).toBe(true)
    applyStatusEvents([
      {
        name: "status",
        data: {
          model: "deepseek-chat",
          fallback: true,
          mode: "full",
          waiting: 0,
          running: 0,
          spend24h: 2.1,
        },
      },
    ])
    expect(status.model).toBe("deepseek-chat")
    expect(status.fallback).toBe(true)
    expect(status.mode).toBe("full")
    expect(status.entry).toBe("primary")
    expect(status.spend24h).toBe(2.1)
    expect(status.stream).not.toBe("signed-out")
  })
})
