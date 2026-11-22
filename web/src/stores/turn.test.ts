import { beforeEach, describe, expect, it, vi } from "vitest"

type Mod = typeof import("./turn")
let t: Mod

const tool = {
  call: "c1",
  name: "terminal",
  emoji: "💻",
  preview: "go test ./...",
  args: '{"command":"go test ./..."}',
}

beforeEach(async () => {
  vi.resetModules()
  t = await import("./turn")
})

describe("turn store", () => {
  it("takes the turn and the queue from hello", () => {
    t.applyTurnEvents([
      {
        name: "hello",
        data: {
          turn: {
            running: true,
            text: "Hi",
            tools: [],
            plan: [],
            queue: [{ kind: "media", preview: "photo.jpg" }],
          },
          cards: [],
        },
      },
    ])
    expect(t.turn.view?.text).toBe("Hi")
    expect(t.turn.queue).toEqual([{ kind: "media", preview: "photo.jpg" }])
    t.applyTurnEvents([{ name: "hello", data: { turn: null, cards: [] } }])
    expect(t.turn.view).toBeNull()
    expect(t.turn.queue).toEqual([])
  })

  it("builds the live turn from the stream events", () => {
    const first = t.turn.view
    t.applyTurnEvents([
      { name: "delta", data: { text: "Look" } },
      { name: "delta", data: { text: "ing." } },
      { name: "tool.start", data: tool },
      {
        name: "progress",
        data: {
          batch: "b1",
          child: "2/3",
          tool: "browser_navigate",
          preview: "https://example.com",
        },
      },
      {
        name: "tool.end",
        data: {
          ...tool,
          state: "error",
          duration_ms: 4200,
          text: "FAIL\n[exit code: 1]",
          cut: false,
        },
      },
      { name: "boundary", data: {} },
      { name: "delta", data: { text: "Fixed." } },
      {
        name: "plan",
        data: { before: [], after: [{ text: "Read the test", state: "completed", parent: 0 }] },
      },
    ])
    const v = t.turn.view!
    expect(v).not.toBe(first)
    expect(v.running).toBe(true)
    expect(v.text).toBe("Looking.\n\nFixed.")
    expect(v.tools).toEqual([
      {
        ...tool,
        preview: '🔀 [2/3] browser_navigate "https://example.com"',
        state: "error",
        duration_ms: 4200,
        text: "FAIL\n[exit code: 1]",
        cut: false,
      },
    ])
    expect(v.plan).toEqual([{ text: "Read the test", state: "completed", parent: 0 }])
  })

  it("omits the quoted part of a progress line with no preview", () => {
    t.applyTurnEvents([
      { name: "tool.start", data: tool },
      { name: "progress", data: { batch: "b1", child: "1/2", tool: "terminal", preview: "" } },
    ])
    expect(t.turn.view!.tools[0].preview).toBe("🔀 [1/2] terminal")
  })

  it("keeps the queue and the acks", () => {
    t.applyTurnEvents([
      { name: "queue", data: { items: [{ kind: "skill", preview: "/release-notes" }], max: 20 } },
      {
        name: "ack",
        data: { id: "a1", kind: "steer", text: "↪️ Got it.", request_id: "", buttons: [] },
      },
      {
        name: "ack",
        data: { id: "a2", kind: "reply", text: "⏸ Paused.", request_id: "r1", buttons: [] },
      },
      {
        name: "ack",
        data: {
          id: "a1",
          kind: "steer",
          text: "✅ The agent read your note.",
          request_id: "",
          buttons: [],
        },
      },
    ])
    expect(t.turn.queue).toEqual([{ kind: "skill", preview: "/release-notes" }])
    expect(t.turn.acks.map((a) => a.text)).toEqual(["✅ The agent read your note.", "⏸ Paused."])
    t.applyTurnEvents([{ name: "turn.done", data: { message_id: 9, error: "", cost: {} } }])
    expect(t.turn.view).toBeNull()
    expect(t.turn.queue).toEqual([])
    expect(t.turn.acks.map((a) => a.id)).toEqual(["a2"])
  })

  it("keeps the last 5 acks", () => {
    t.applyTurnEvents(
      [1, 2, 3, 4, 5, 6, 7].map((i) => ({
        name: "ack",
        data: { id: "a" + i, kind: "reply", text: String(i), request_id: "r" + i, buttons: [] },
      })),
    )
    expect(t.turn.acks.map((a) => a.id)).toEqual(["a3", "a4", "a5", "a6", "a7"])
  })

  it("keeps the turn.done data until a new turn starts", () => {
    t.applyTurnEvents([
      { name: "turn.done", data: { message_id: 9, error: "", cost: { usd: 0.012 } } },
    ])
    expect(t.turn.done?.cost.usd).toBe(0.012)
    t.applyTurnEvents([{ name: "delta", data: { text: "Next" } }])
    expect(t.turn.done).toBeNull()
    expect(t.turn.view?.text).toBe("Next")
  })
})
