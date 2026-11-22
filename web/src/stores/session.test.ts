import { beforeEach, describe, expect, it, vi } from "vitest"
import { flushPromises } from "@vue/test-utils"

vi.mock("../api/client", () => ({ api: vi.fn() }))

type Mod = typeof import("./session")
let s: Mod
let api: ReturnType<typeof vi.fn>

const msg = (id: number, role = "assistant", text = "m" + id) => ({
  id,
  role,
  text,
  reasoning: "",
  reasoning_tokens: 0,
  model: "",
  provider: "",
  in: 0,
  out: 0,
  cached: 0,
  latency_ms: 0,
  first_ms: 0,
  cost_usd: 0,
  unpriced: false,
  answered_by: "",
  compacted: false,
  files: [],
  tools: [],
})
const range = (a: number, b: number) => Array.from({ length: b - a + 1 }, (_, i) => msg(a + i))
// Wraps views in a page. The cursors default to the smallest and the largest positive view id.
const pg = (
  rows: ReturnType<typeof msg>[],
  o: { first?: number; last?: number; older?: boolean; newer?: boolean } = {},
) => {
  const ids = rows.map((m) => m.id).filter((n) => n > 0)
  return {
    rows,
    first_id: o.first ?? (ids.length > 0 ? Math.min(...ids) : 0),
    last_id: o.last ?? (ids.length > 0 ? Math.max(...ids) : 0),
    has_older: o.older ?? false,
    has_newer: o.newer ?? false,
  }
}
const tool = (call: string, text: string) => ({
  call,
  name: "terminal",
  emoji: "",
  preview: "",
  args: "{}",
  state: "done",
  duration_ms: 0,
  text,
  cut: false,
})
const hello = (session_id: string, last_message_id: number) => ({
  name: "hello",
  data: {
    turn: null,
    cards: [],
    status: {},
    session_id,
    last_message_id,
    notices: [],
    build_id: "b1",
  },
})

beforeEach(async () => {
  vi.resetModules()
  s = await import("./session")
  api = (await import("../api/client")).api as ReturnType<typeof vi.fn>
  api.mockReset()
})

describe("session store", () => {
  it("loads the newest page for a new session id", async () => {
    api.mockImplementation(async (_m: string, path: string) =>
      path === "/api/sessions/s1"
        ? { id: "s1", title: "Release notes" }
        : pg(range(451, 500), { older: true }),
    )
    s.applySessionEvents([hello("s1", 500)])
    await flushPromises()
    expect(api).toHaveBeenCalledWith("GET", "/api/sessions/s1/messages?limit=50")
    expect(s.session.id).toBe("s1")
    expect(s.session.title).toBe("Release notes")
    expect(s.session.messages.map((m) => m.id)).toEqual(range(451, 500).map((m) => m.id))
    expect(s.session.lastId).toBe(500)
    expect(s.session.firstId).toBe(451)
    expect(s.session.hasOlder).toBe(true)
  })

  it("takes the session fields from the hello snapshot", async () => {
    api.mockResolvedValue(pg([]))
    s.applySessionEvents([
      {
        name: "hello",
        data: {
          ...hello("s1", 0).data,
          session: {
            id: "s1",
            title: "T",
            model: "m",
            effort: "low",
            goal_line: "g",
            context: null,
          },
        },
      },
    ])
    await flushPromises()
    expect(s.session.title).toBe("T")
    expect(s.session.model).toBe("m")
    expect(s.session.effort).toBe("low")
    expect(s.session.goalLine).toBe("g")
    expect(api).not.toHaveBeenCalledWith("GET", "/api/sessions/s1")
  })

  it("shows the final answer once after a hello with no turn", async () => {
    s.session.id = "s1"
    s.session.messages = [msg(40, "user")]
    s.session.lastId = 40
    api.mockResolvedValueOnce(pg([msg(41), msg(42)]))
    s.applySessionEvents([hello("s1", 42)])
    await flushPromises()
    expect(api).toHaveBeenCalledWith("GET", "/api/sessions/s1/messages?after=40&limit=50")
    api.mockResolvedValueOnce(pg([msg(42)]))
    s.session.lastId = 41
    await s.loadMessages()
    expect(s.session.messages.map((m) => m.id)).toEqual([40, 41, 42])
    api.mockClear()
    s.applySessionEvents([hello("s1", 42)])
    await flushPromises()
    expect(api).not.toHaveBeenCalled()
  })

  it("loads newer messages in pages of 50", async () => {
    s.session.id = "s1"
    s.session.messages = [msg(100)]
    s.session.lastId = 100
    api
      .mockResolvedValueOnce(pg(range(101, 150), { older: true, newer: true }))
      .mockResolvedValueOnce(pg(range(151, 153), { older: true }))
    await s.loadMessages()
    expect(api.mock.calls.map((c) => c[1])).toEqual([
      "/api/sessions/s1/messages?after=100&limit=50",
      "/api/sessions/s1/messages?after=150&limit=50",
    ])
    expect(s.session.messages).toHaveLength(54)
    expect(s.session.lastId).toBe(153)
  })

  it("puts an older page first", async () => {
    s.session.id = "s1"
    s.session.messages = [msg(451), msg(452)]
    s.session.lastId = 452
    api.mockResolvedValueOnce(pg([msg(449), msg(450)], { older: true }))
    await s.loadMessages({ before: 451 })
    expect(api).toHaveBeenCalledWith("GET", "/api/sessions/s1/messages?before=451&limit=50")
    expect(s.session.messages.map((m) => m.id)).toEqual([449, 450, 451, 452])
  })

  it("applies the session and goal events", async () => {
    s.session.id = "s1"
    s.session.messages = [msg(1)]
    s.session.lastId = 1
    api.mockResolvedValue(pg([]))
    s.applySessionEvents([
      {
        name: "session",
        data: {
          id: "s2",
          title: "New",
          model: "glm-4.6",
          effort: "high",
          goal_line: "",
          context: { tokens: 38200, window: 200000, threshold: 0.5 },
        },
      },
      { name: "goal", data: { line: "↻ Goal 8/20: 2 tests still fail" } },
    ])
    await flushPromises()
    expect(s.session.id).toBe("s2")
    expect(s.session.title).toBe("New")
    expect(s.session.model).toBe("glm-4.6")
    expect(s.session.effort).toBe("high")
    expect(s.session.context).toEqual({ tokens: 38200, window: 200000, threshold: 0.5 })
    expect(s.session.goalLine).toBe("↻ Goal 8/20: 2 tests still fail")
    expect(s.session.messages).toEqual([])
    expect(s.session.lastId).toBe(0)
  })

  it("loads the saved answer after turn.done", async () => {
    s.session.id = "s1"
    s.session.messages = [msg(7, "user")]
    s.session.lastId = 7
    api.mockResolvedValueOnce(pg([msg(8)]))
    s.applySessionEvents([{ name: "turn.done", data: { message_id: 8, error: "", cost: {} } }])
    await flushPromises()
    expect(api).toHaveBeenCalledWith("GET", "/api/sessions/s1/messages?after=7&limit=50")
    expect(s.session.messages.map((m) => m.id)).toEqual([7, 8])
  })

  it("loads the user message at the first live event of a turn, once", async () => {
    s.session.id = "s1"
    s.session.messages = [msg(7)]
    s.session.lastId = 7
    api.mockResolvedValue(pg([msg(8, "user")]))
    s.applySessionEvents([{ name: "delta", data: "Hel" }])
    await flushPromises()
    expect(api).toHaveBeenCalledTimes(1)
    expect(api).toHaveBeenCalledWith("GET", "/api/sessions/s1/messages?after=7&limit=50")
    expect(s.session.messages.map((m) => m.id)).toEqual([7, 8])
    s.applySessionEvents([
      { name: "delta", data: "lo" },
      { name: "tool.start", data: {} },
    ])
    await flushPromises()
    expect(api).toHaveBeenCalledTimes(1)
  })

  it("loads after the last id when turn.done has message id 0", async () => {
    s.session.id = "s1"
    s.session.messages = [msg(7, "user")]
    s.session.lastId = 7
    api.mockResolvedValueOnce(pg([]))
    s.applySessionEvents([{ name: "turn.done", data: { message_id: 0, error: "", cost: {} } }])
    await flushPromises()
    expect(api).toHaveBeenCalledWith("GET", "/api/sessions/s1/messages?after=7&limit=50")
  })
  it("keeps a negative-id summary row after the message it follows", async () => {
    api.mockResolvedValue(pg([msg(5), { ...msg(-2, "summary"), compacted: true }, msg(6), msg(7)]))
    s.applySessionEvents([hello("s1", 7)])
    await flushPromises()
    expect(s.session.messages.map((m) => m.id)).toEqual([5, -2, 6, 7])
    expect(s.session.lastId).toBe(7)
  })

  it("keeps the summary place and the last id when a page is added", async () => {
    s.session.id = "s1"
    s.session.messages = [msg(5), { ...msg(-2, "summary"), compacted: true }, msg(6)] as never
    s.session.lastId = 6
    api.mockResolvedValueOnce(pg([msg(7), msg(8)]))
    await s.loadMessages()
    expect(s.session.messages.map((m) => m.id)).toEqual([5, -2, 6, 7, 8])
    api.mockResolvedValueOnce(pg([msg(3), msg(4), { ...msg(-1, "summary"), compacted: true }]))
    await s.loadMessages({ before: 5 })
    expect(s.session.messages.map((m) => m.id)).toEqual([3, 4, -1, 5, -2, 6, 7, 8])
    expect(s.session.lastId).toBe(8)
  })

  it("uses the last positive id when the newest row is a summary", async () => {
    api.mockResolvedValue(pg([msg(5), { ...msg(-2, "summary"), compacted: true }]))
    s.applySessionEvents([hello("s1", 5)])
    await flushPromises()
    expect(s.session.lastId).toBe(5)
  })
  it("loads newer pages until has_newer is false, also when tool results shrink a page", async () => {
    s.session.id = "s1"
    s.session.messages = [msg(10, "user")]
    s.session.lastId = 10
    // 50 saved rows: 25 calls and their 25 results merge to 25 views. The last raw id is 60.
    const calls = range(11, 35).map((m) => ({ ...m, tools: [tool("c" + m.id, "out")] }))
    api
      .mockResolvedValueOnce(pg(calls, { first: 11, last: 60, older: true, newer: true }))
      .mockResolvedValueOnce(pg([msg(61, "assistant", "final answer")], { older: true }))
    await s.loadMessages()
    expect(api.mock.calls.map((c) => c[1])).toEqual([
      "/api/sessions/s1/messages?after=10&limit=50",
      "/api/sessions/s1/messages?after=60&limit=50",
    ])
    expect(s.session.messages.at(-1)?.text).toBe("final answer")
    expect(s.session.lastId).toBe(61)
  })

  it("replaces a call when a newer page sends it again with its results", async () => {
    s.session.id = "s1"
    s.session.messages = [msg(7, "user"), { ...msg(8), tools: [tool("c1", "")] }] as never
    s.session.lastId = 8
    api.mockResolvedValueOnce(
      pg([{ ...msg(8), tools: [tool("c1", "out")] }], { first: 8, last: 9 }),
    )
    await s.loadMessages()
    expect(s.session.messages.map((m) => m.id)).toEqual([7, 8])
    expect(s.session.messages[1].tools[0].text).toBe("out")
    expect(s.session.lastId).toBe(9)
    expect(api).toHaveBeenCalledTimes(1)
  })

  it("does not load again when the last saved id is a tool result", async () => {
    s.session.id = "s1"
    api.mockResolvedValueOnce(
      pg([msg(5, "user"), { ...msg(6), tools: [tool("c1", "out")] }], { last: 7 }),
    )
    s.applySessionEvents([hello("s1", 7)])
    await flushPromises()
    expect(s.session.lastId).toBe(7)
    api.mockClear()
    s.applySessionEvents([hello("s1", 7)])
    await flushPromises()
    expect(api).not.toHaveBeenCalled()
  })

  it("loads an older page before the first saved id and keeps has_older", async () => {
    s.session.id = "s1"
    api.mockResolvedValueOnce(pg([msg(20), msg(21)], { first: 19, older: true }))
    s.applySessionEvents([hello("s1", 21)])
    await flushPromises()
    expect(s.session.firstId).toBe(19)
    expect(s.session.hasOlder).toBe(true)
    api.mockResolvedValueOnce(pg([msg(17, "user"), msg(18)]))
    await s.loadMessages({ before: 20 })
    expect(api).toHaveBeenLastCalledWith("GET", "/api/sessions/s1/messages?before=19&limit=50")
    expect(s.session.messages.map((m) => m.id)).toEqual([17, 18, 20, 21])
    expect(s.session.firstId).toBe(17)
    expect(s.session.hasOlder).toBe(false)
  })
})
