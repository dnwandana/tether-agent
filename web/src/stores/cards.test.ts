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
const answer = { answer: "once", choices: [], text: "", value: "" }

beforeEach(async () => {
  vi.resetModules()
  c = await import("./cards")
  client = await import("../api/client")
  ;(client.api as ReturnType<typeof vi.fn>).mockReset()
})

describe("cards store", () => {
  it("follows hello, card, and card.done", () => {
    c.applyCardEvents([{ name: "hello", data: { turn: null, cards: [card("a1")] } }])
    c.applyCardEvents([
      { name: "card", data: card("a2") },
      { name: "card", data: card("a2") },
    ])
    expect(c.cards.open.map((x) => x.id)).toEqual(["a1", "a2"])
    c.applyCardEvents([
      { name: "card.done", data: { id: "a1", header: "✅ Approval allowed · once" } },
    ])
    expect(c.cards.open.map((x) => x.id)).toEqual(["a2"])
  })

  it("posts an answer", async () => {
    ;(client.api as ReturnType<typeof vi.fn>).mockResolvedValue(undefined)
    await c.answerCard("a1", answer)
    expect(client.api).toHaveBeenCalledWith("POST", "/api/cards/a1", answer)
  })

  it("closes a card that another page answered first", async () => {
    c.applyCardEvents([{ name: "hello", data: { turn: null, cards: [card("a1")] } }])
    ;(client.api as ReturnType<typeof vi.fn>).mockRejectedValue(
      new client.ApiError(409, "This card is already answered."),
    )
    await expect(c.answerCard("a1", answer)).rejects.toThrow("This card is already answered.")
    expect(c.cards.open).toEqual([])
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
