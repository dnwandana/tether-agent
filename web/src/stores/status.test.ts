import { describe, expect, it } from "vitest"
import { applyStatusEvents, status } from "./status"

describe("status store", () => {
  it("starts as reconnecting", () => {
    expect(status.stream).toBe("reconnecting")
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
    expect(status.stream).toBe("reconnecting")
  })
})
