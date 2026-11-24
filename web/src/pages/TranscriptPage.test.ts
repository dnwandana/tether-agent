import { describe, it, expect, vi, beforeEach } from "vitest"
import { mount, flushPromises } from "@vue/test-utils"
import { createRouter, createMemoryHistory } from "vue-router"

vi.mock("../api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api/client")>()),
  api: vi.fn(),
  runCommand: vi.fn(),
}))
vi.mock("../stores/ui", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../stores/ui")>()),
  toast: vi.fn(),
  confirm: vi.fn(),
}))
import { api, runCommand } from "../api/client"
import TranscriptPage from "./TranscriptPage.vue"
import { fullTime, shortTime } from "../lib/time"

const Blank = { render: () => null }
const info = {
  id: "s2",
  title: "Photos",
  kind: "chat",
  source: "telegram",
  source_label: "Telegram › DM",
  last_active: "2026-11-20T09:00:00Z",
  started: "2026-11-19T09:00:00Z",
  messages: 51,
  cost_usd: 0.3104,
  archived: false,
  read_only: true,
  children: [],
  parent_id: "",
}
const msg = (o: object) => ({
  id: 0,
  created: "2026-11-20T09:05:00Z",
  role: "user",
  text: "",
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
  ...o,
})
const msgs = [
  msg({ id: 10, compacted: true, text: "An old question." }),
  msg({ id: -4, role: "summary", compacted: true, text: "Summary of 41 earlier messages." }),
  msg({ id: 11, text: "Find the flaky test." }),
  msg({
    id: 12,
    role: "assistant",
    text: "Runs() had no offset.",
    model: "deepseek-chat",
    provider: "backup",
    in: 40300,
    cached: 0,
    out: 655,
    first_ms: 1900,
    latency_ms: 9700,
    cost_usd: 0.0061,
    unpriced: true,
    answered_by: "backup",
    reasoning: "no offset",
    reasoning_tokens: 812,
    tools: [
      {
        call: "c1",
        name: "patch",
        emoji: "🔧",
        preview: "internal/cron/jobs.go",
        args: "{}",
        state: "done",
        duration_ms: 3,
        text: "",
        cut: false,
      },
    ],
  }),
]

// Wraps views in a page. The cursors default to the smallest and the largest positive view id.
const pg = (
  rows: object[],
  o: { first?: number; last?: number; older?: boolean; newer?: boolean } = {},
) => {
  const ids = rows.map((m) => (m as { id: number }).id).filter((n) => n > 0)
  return {
    rows,
    first_id: o.first ?? (ids.length > 0 ? Math.min(...ids) : 0),
    last_id: o.last ?? (ids.length > 0 ? Math.max(...ids) : 0),
    has_older: o.older ?? false,
    has_newer: o.newer ?? false,
  }
}
const olderButton = (w: Awaited<ReturnType<typeof mk>>["w"]) =>
  w.findAll("button").find((b) => b.text() === "Load older")

async function mk(path = "/sessions/s2") {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/sessions", name: "sessions", component: Blank },
      { path: "/sessions/:id", name: "transcript", component: TranscriptPage },
      { path: "/", name: "chat", component: Blank },
    ],
  })
  await router.push(path)
  const w = mount(TranscriptPage, { global: { plugins: [router] } })
  await flushPromises()
  return { w, router }
}

beforeEach(() => {
  vi.mocked(api)
    .mockReset()
    .mockImplementation(
      async (_m: string, p: string) => (p === "/api/sessions/s2" ? info : pg(msgs)) as any,
    )
  vi.mocked(runCommand)
    .mockReset()
    .mockResolvedValue({ request_id: "r", text: "Forked.", buttons: [], edit: false })
})

describe("TranscriptPage", () => {
  it("loads the session and the last page of messages", async () => {
    const { w } = await mk()
    expect(api).toHaveBeenCalledWith("GET", "/api/sessions/s2")
    expect(api).toHaveBeenCalledWith("GET", "/api/sessions/s2/messages?limit=50")
    expect(w.findAll("details.card")).toHaveLength(1)
    expect(w.find("details.card summary").text()).toContain("🗜 Compacted")
    expect(w.find("#msg-10 .bubble-user").text()).toBe("An old question.")
    expect(w.find("#msg-11 .bubble-user").text()).toBe("Find the flaky test.")
    expect(w.find("details.reasoning summary").text()).toBe("▸ Reasoning · 812 tokens")
    const foot = w.find(".cost-foot").text()
    for (const s of [
      "deepseek-chat · answered by backup",
      "40.3k in (0 cached)",
      "655 out",
      "$0.0061+",
      "10 s",
    ])
      expect(foot).toContain(s)
  })

  it("loads older messages before the smallest positive id", async () => {
    vi.mocked(api).mockImplementation(
      async (_m: string, p: string) =>
        (p === "/api/sessions/s2"
          ? info
          : p.includes("before=")
            ? pg([])
            : pg(
                Array.from({ length: 50 }, (_, i) =>
                  msg({
                    id: i === 0 ? -9 : i + 20,
                    role: i === 0 ? "summary" : "user",
                    text: "m" + i,
                  }),
                ),
                { older: true },
              )) as any,
    )
    const { w } = await mk()
    await w
      .findAll("button")
      .find((b) => b.text() === "Load older")!
      .trigger("click")
    expect(api).toHaveBeenCalledWith("GET", "/api/sessions/s2/messages?before=21&limit=50")
  })

  it("shows the read-only banner and forks into the dashboard", async () => {
    const { w } = await mk()
    expect(w.find(".ro-banner").text()).toContain(
      "👁 Read-only. This session belongs to Telegram › DM.",
    )
    await w
      .findAll("button")
      .find((b) => b.text() === "Fork into dashboard")!
      .trigger("click")
    expect(runCommand).toHaveBeenCalledWith("/fork from s2")
  })

  it("opens the messages around a search hit", async () => {
    await mk("/sessions/s2?message=11")
    expect(api).toHaveBeenCalledWith("GET", "/api/sessions/s2/messages?around=11&limit=25")
  })

  it("shows Load older from has_older when tool results make the page short", async () => {
    // 50 saved rows merge to 25 views. The first saved id is 30.
    const views = Array.from({ length: 25 }, (_, i) =>
      msg({ id: 31 + 2 * i, role: "assistant", text: "a" + i }),
    )
    const older = pg([msg({ id: 29, text: "first question" })])
    vi.mocked(api).mockImplementation(
      async (_m: string, p: string) =>
        (p === "/api/sessions/s2"
          ? info
          : p.includes("before=")
            ? older
            : pg(views, { first: 30, last: 80, older: true })) as any,
    )
    const { w } = await mk()
    expect(olderButton(w)).toBeDefined()
    await olderButton(w)!.trigger("click")
    await flushPromises()
    expect(api).toHaveBeenCalledWith("GET", "/api/sessions/s2/messages?before=30&limit=50")
    expect(w.find("#msg-29 .bubble-user").text()).toBe("first question")
    expect(olderButton(w)).toBeUndefined()
  })

  it("hides Load older when the page has no older messages", async () => {
    const full = Array.from({ length: 50 }, (_, i) => msg({ id: i + 1, text: "m" + i }))
    vi.mocked(api).mockImplementation(
      async (_m: string, p: string) => (p === "/api/sessions/s2" ? info : pg(full)) as any,
    )
    const { w } = await mk()
    expect(olderButton(w)).toBeUndefined()
  })

  it("does not add a row that shows already", async () => {
    vi.mocked(api).mockImplementation(
      async (_m: string, p: string) =>
        (p === "/api/sessions/s2"
          ? info
          : p.includes("before=")
            ? pg([msg({ id: 9, text: "older" }), msg({ id: 10, text: "again" })])
            : pg([msg({ id: 10, text: "An old question." })], { older: true })) as any,
    )
    const { w } = await mk()
    await olderButton(w)!.trigger("click")
    await flushPromises()
    expect(w.findAll("#msg-10")).toHaveLength(1)
    expect(w.find("#msg-9 .bubble-user").text()).toBe("older")
  })

  it("loads newer messages after the last id", async () => {
    vi.mocked(api).mockImplementation(
      async (_m: string, p: string) =>
        (p === "/api/sessions/s2"
          ? info
          : p.includes("after=")
            ? pg([msg({ id: 90, text: "a newer question" })])
            : pg(msgs, { last: 80, newer: true })) as any,
    )
    const { w } = await mk()
    await w
      .findAll("button")
      .find((b) => b.text() === "Load newer")!
      .trigger("click")
    await flushPromises()
    expect(api).toHaveBeenCalledWith("GET", "/api/sessions/s2/messages?after=80&limit=50")
    expect(w.find("#msg-90 .bubble-user").text()).toBe("a newer question")
    expect(w.findAll("button").some((b) => b.text() === "Load newer")).toBe(false)
  })

  it("drops a newer page that arrives after a route change", async () => {
    let release!: (v: unknown) => void
    const late = new Promise((r) => (release = r))
    vi.mocked(api).mockImplementation(async (_m: string, p: string) => {
      if (p.includes("after=")) return (await late) as any
      if (p === "/api/sessions/s2") return info as any
      if (p === "/api/sessions/s3") return { ...info, id: "s3" } as any
      if (p.startsWith("/api/sessions/s3/"))
        return pg([msg({ id: 5, text: "other session" })]) as any
      return pg(msgs, { last: 80, newer: true }) as any
    })
    const { w, router } = await mk()
    await w
      .findAll("button")
      .find((b) => b.text() === "Load newer")!
      .trigger("click")
    await router.push("/sessions/s3")
    await flushPromises()
    release(pg([msg({ id: 90, text: "a newer question" })]))
    await flushPromises()
    expect(w.text()).toContain("other session")
    expect(w.text()).not.toContain("a newer question")
  })

  it("marks the hit and jumps to the latest messages", async () => {
    const { w, router } = await mk("/sessions/s2?message=11")
    const hitMsg = w.find("#msg-11")
    expect(hitMsg.classes()).toContain("is-hit")
    expect(hitMsg.text()).toContain("Search hit")
    await w
      .findAll("button")
      .find((b) => b.text() === "Jump to latest")!
      .trigger("click")
    await flushPromises()
    expect(router.currentRoute.value.query).toEqual({})
    expect(api).toHaveBeenLastCalledWith("GET", "/api/sessions/s2/messages?limit=50")
  })

  it("shows the time of each message", async () => {
    const { w } = await mk()
    expect(w.find("#msg-11 time").attributes("datetime")).toBe("2026-11-20T09:05:00Z")
  })

  it("links the children and the parent", async () => {
    const child = { ...info, id: "c1", title: "subagent run", kind: "subagent", children: [] }
    vi.mocked(api).mockImplementation(
      async (_m: string, p: string) =>
        (p === "/api/sessions/s2"
          ? { ...info, parent_id: "p1", children: [child] }
          : p === "/api/sessions/p1"
            ? { ...info, id: "p1", title: "Release notes" }
            : pg(msgs)) as any,
    )
    const { w } = await mk()
    expect(api).toHaveBeenCalledWith("GET", "/api/sessions/p1")
    expect(w.find("a[href='/sessions/c1']").text()).toBe("subagent run")
    const parent = w.find("a[href='/sessions/p1']")
    expect(parent.text()).toBe("↳ child of Release notes")
  })

  it("opens the arguments and the result of a tool call", async () => {
    vi.mocked(api).mockImplementation(
      async (_m: string, p: string) =>
        (p === "/api/sessions/s2"
          ? info
          : p === "/api/sessions/s2/tools/c1"
            ? {
                call: "c1",
                name: "patch",
                emoji: "🔧",
                preview: "internal/cron/jobs.go",
                args: '{"path":"internal/cron/jobs.go"}',
                state: "done",
                duration_ms: 3,
                text: "--- a\n+++ b\n",
                cut: false,
              }
            : pg(msgs)) as any,
    )
    const { w } = await mk()
    await w.find("button.toolline").trigger("click")
    await flushPromises()
    expect(api).toHaveBeenCalledWith("GET", "/api/sessions/s2/tools/c1")
    expect(w.find(".tool-args").text()).toContain('"path": "internal/cron/jobs.go"')
    expect(w.find(".tool-result").exists()).toBe(true)
  })

  it("shows the empty text for a session with no messages", async () => {
    vi.mocked(api).mockImplementation(
      async (_m: string, p: string) => (p === "/api/sessions/s2" ? info : pg([])) as any,
    )
    const { w } = await mk()
    expect(w.text()).toContain("No messages yet.")
  })

  it("loads again from the action of the error state", async () => {
    vi.mocked(api).mockRejectedValueOnce(new Error("HTTP 500"))
    const { w } = await mk()
    const block = w.findComponent({ name: "StateBlock" })
    expect(block.props("state")).toBe("error")
    block.vm.$emit("action")
    await flushPromises()
    expect(w.text()).toContain("Photos")
  })

  it("goes to Chat only when the fork reply is ok", async () => {
    vi.mocked(runCommand).mockResolvedValueOnce({
      request_id: "r",
      text: "⏳ **Busy**\n`/fork` cannot run during a task. Wait for the answer, or stop the task.",
      buttons: [[{ text: "⏹ Stop the task", data: "/stop" }]],
      edit: false,
    })
    const { w, router } = await mk()
    await w
      .findAll("button")
      .find((b) => b.text() === "Fork into dashboard")!
      .trigger("click")
    await flushPromises()
    expect(router.currentRoute.value.name).toBe("transcript")
  })

  it("revokes the export URL after a delay", async () => {
    vi.useFakeTimers()
    const { createObjectURL, revokeObjectURL } = URL
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {})
    try {
      const revoke = vi.fn()
      URL.createObjectURL = vi.fn(() => "blob:x")
      URL.revokeObjectURL = revoke
      vi.stubGlobal(
        "fetch",
        vi.fn(async () => ({ ok: true, blob: async () => new Blob(["x"]) })),
      )
      const { w } = await mk()
      await w
        .findAll("button")
        .find((b) => b.text() === "Export JSONL")!
        .trigger("click")
      await flushPromises()
      expect(revoke).not.toHaveBeenCalled()
      vi.advanceTimersByTime(30_000)
      expect(revoke).toHaveBeenCalledWith("blob:x")
      expect(click).toHaveBeenCalled()
    } finally {
      URL.createObjectURL = createObjectURL
      URL.revokeObjectURL = revokeObjectURL
      click.mockRestore()
      vi.useRealTimers()
      vi.unstubAllGlobals()
    }
  })

  it("shows message and start times in the shared format", async () => {
    const { w } = await mk()
    const t = w.find("#msg-11 time")
    expect(t.text()).toBe(shortTime("2026-11-20T09:05:00Z"))
    expect(t.attributes("title")).toBe(fullTime("2026-11-20T09:05:00Z"))
    expect(t.attributes("datetime")).toBe("2026-11-20T09:05:00Z")
    expect(w.text()).toContain(shortTime("2026-11-19T09:00:00Z"))
    const started = w.find('[data-test="started"]')
    expect(started.text()).toBe(shortTime("2026-11-19T09:00:00Z"))
    expect(started.attributes("title")).toBe(fullTime("2026-11-19T09:00:00Z"))
    expect(started.attributes("style")).toContain("white-space: nowrap")
  })
})
