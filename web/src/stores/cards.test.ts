import { beforeEach, describe, expect, it, vi } from "vitest"

vi.mock("../api/client", () => {
  class ApiError extends Error {
    status: number
    constructor(status: number, message: string) {
      super(message)
      this.status = status
    }
  }
  return { api: vi.fn(), ApiError }
})

type Mod = typeof import("./cards")
let c: Mod
let client: typeof import("../api/client")

const card = (id: string, kind = "approval") => ({
  id,
  kind,
  header: "🛡 Approval needed",
  command: "rm -rf build/",
})
const rec = (id: string, at: string, session_id = "s1", kind = "approval") => ({
  id,
  kind,
  header: "✅ Approval allowed · once",
  preview: "rm -rf build/",
  session_id,
  at,
})
const answer = { answer: "once", choices: [], text: "", value: "" }

beforeEach(async () => {
  vi.resetModules()
  c = await import("./cards")
  client = await import("../api/client")
  ;(client.api as ReturnType<typeof vi.fn>).mockReset()
})

describe("cards store", () => {
  it("follows hello, card, and card.done, and moves an answered card to answered", () => {
    c.applyCardEvents([
      {
        name: "hello",
        data: { turn: null, cards: [card("a1")], answered: [rec("r0", "2026-11-23T09:00:00Z")] },
      },
    ])
    c.applyCardEvents([
      { name: "card", data: card("a2") },
      { name: "card", data: card("a2") },
    ])
    expect(c.cards.open.map((x) => x.id)).toEqual(["a1", "a2"])
    c.applyCardEvents([{ name: "card.done", data: rec("a1", "2026-11-23T09:05:00Z") }])
    expect(c.cards.open.map((x) => x.id)).toEqual(["a2"])
    expect(c.cards.answered.map((x) => x.id)).toEqual(["a1", "r0"])
    c.applyCardEvents([{ name: "card.done", data: rec("a1", "2026-11-23T09:05:00Z") }])
    expect(c.cards.answered.map((x) => x.id)).toEqual(["a1", "r0"])
  })

  it("keeps at most 30 answered cards, newest first", () => {
    const many = Array.from({ length: 31 }, (_, i) =>
      rec("r" + i, new Date(Date.UTC(2026, 10, 23, 9, i)).toISOString()),
    )
    c.applyCardEvents([{ name: "hello", data: { turn: null, cards: [], answered: many } }])
    expect(c.cards.answered).toHaveLength(30)
    expect(c.cards.answered[0].id).toBe("r30")
    expect(c.cards.answered[29].id).toBe("r1")
  })

  it("posts an answer", async () => {
    ;(client.api as ReturnType<typeof vi.fn>).mockResolvedValue(undefined)
    await c.answerCard("a1", answer)
    expect(client.api).toHaveBeenCalledWith("POST", "/api/cards/a1", answer)
  })

  it("does not throw on a 409 when card.done is already there", async () => {
    c.applyCardEvents([{ name: "hello", data: { turn: null, cards: [card("a1")], answered: [] } }])
    c.cards.answered = [rec("a1", "2026-11-23T09:05:00Z")] as never
    ;(client.api as ReturnType<typeof vi.fn>).mockRejectedValueOnce(
      new client.ApiError(409, "This card is already answered."),
    )
    await expect(c.answerCard("a1", answer)).resolves.toBeUndefined()
    expect(c.cards.open).toEqual([])
    expect(client.api).toHaveBeenCalledTimes(1)
  })

  it("reads the cards after a 409 when card.done is not there yet", async () => {
    c.applyCardEvents([
      { name: "hello", data: { turn: null, cards: [card("a1"), card("a2")], answered: [] } },
    ])
    const api = client.api as ReturnType<typeof vi.fn>
    api.mockRejectedValueOnce(new client.ApiError(409, "This card is already answered."))
    api.mockResolvedValueOnce({ open: [card("a2")], answered: [rec("a1", "2026-11-23T09:05:00Z")] })
    await expect(c.answerCard("a1", answer)).resolves.toBeUndefined()
    expect(api).toHaveBeenLastCalledWith("GET", "/api/cards")
    expect(c.cards.open.map((x) => x.id)).toEqual(["a2"])
    expect(c.cards.answered.map((x) => x.id)).toEqual(["a1"])
  })

  it("still throws an error that is not a 409", async () => {
    ;(client.api as ReturnType<typeof vi.fn>).mockRejectedValueOnce(
      new client.ApiError(500, "boom"),
    )
    await expect(c.answerCard("a1", answer)).rejects.toThrow("boom")
  })

  it("posts the shown ids for a bulk answer", async () => {
    ;(client.api as ReturnType<typeof vi.fn>).mockResolvedValue({ answered: 2 })
    expect(await c.bulkAnswer(["a1", "a3"], "deny")).toBe(2)
    expect(client.api).toHaveBeenCalledWith("POST", "/api/cards/bulk", {
      answer: "deny",
      ids: ["a1", "a3"],
    })
  })
})
