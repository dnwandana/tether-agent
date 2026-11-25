import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { flushPromises } from "@vue/test-utils"
import { cleanup, render, screen, within } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import { defineComponent, h } from "vue"
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
import { rememberListQuery } from "../stores/ui"

const scrollToMessage = vi.fn(() => true)
vi.mock("@/components/ui/message-scroller", async (orig) => ({
  ...(await orig<object>()),
  useMessageScroller: () => ({ scrollToMessage, scrollToEnd: vi.fn(), scrollToStart: vi.fn() }),
}))
// A stub that shows only the call id of the tool.
const ToolResult = defineComponent({
  props: ["tool"],
  setup: (p) => () => h("div", { "data-testid": "tool-result" }, p.tool.call),
})

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
  const r = render(TranscriptPage, { global: { plugins: [router], stubs: { ToolResult } } })
  await flushPromises()
  return { ...r, router, c: r.container as HTMLElement }
}
const item = (c: Element, id: number) => c.querySelector(`[data-message-id="${id}"]`) as HTMLElement
const btn = (name: string) => screen.queryByRole("button", { name })

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

afterEach(() => {
  cleanup()
  scrollToMessage.mockClear()
})

describe("TranscriptPage", () => {
  it("loads the session and the last page of messages", async () => {
    const { c } = await mk()
    expect(api).toHaveBeenCalledWith("GET", "/api/sessions/s2")
    expect(api).toHaveBeenCalledWith("GET", "/api/sessions/s2/messages?limit=50")
    const compacted = screen.getByRole("button", { name: "Compacted · summary" })
    expect(compacted.querySelector("svg.lucide-chevron-right")).not.toBeNull()
    expect(
      compacted.closest('[data-slot="marker"]')!.querySelector("svg.lucide-fold-vertical"),
    ).not.toBeNull()
    await userEvent.setup().click(compacted)
    expect(compacted.querySelector("svg.lucide-chevron-down")).not.toBeNull()
    expect(within(item(c, 10)).getByText("An old question.")).toBeTruthy()
    expect(within(item(c, 11)).getByText("Find the flaky test.")).toBeTruthy()
    expect(screen.getByRole("button", { name: /Reasoning\s*812 tokens/ })).toBeTruthy()
    const foot = item(c, 12).textContent ?? ""
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
    const user = userEvent.setup()
    await mk()
    await user.click(btn("Load older")!)
    expect(api).toHaveBeenCalledWith("GET", "/api/sessions/s2/messages?before=21&limit=50")
  })

  it("shows the read-only banner and forks into the dashboard", async () => {
    const user = userEvent.setup()
    await mk()
    const note = screen.getByText("Read-only. This session belongs to Telegram › DM.")
    expect(note.closest('[role="alert"]')!.querySelector("svg.lucide-eye")).not.toBeNull()
    await user.click(btn("Fork into dashboard")!)
    expect(runCommand).toHaveBeenCalledWith("/fork from s2")
  })

  it("opens the messages around a search hit", async () => {
    await mk("/sessions/s2?message=11")
    expect(api).toHaveBeenCalledWith("GET", "/api/sessions/s2/messages?around=11&limit=25")
    expect(scrollToMessage).toHaveBeenCalledWith("11", { align: "center" })
  })

  it("does not jump when the route has no search hit", async () => {
    await mk()
    expect(scrollToMessage).not.toHaveBeenCalled()
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
    const user = userEvent.setup()
    const { c } = await mk()
    expect(btn("Load older")).not.toBeNull()
    await user.click(btn("Load older")!)
    await flushPromises()
    expect(api).toHaveBeenCalledWith("GET", "/api/sessions/s2/messages?before=30&limit=50")
    expect(within(item(c, 29)).getByText("first question")).toBeTruthy()
    expect(btn("Load older")).toBeNull()
  })

  it("hides Load older when the page has no older messages", async () => {
    const full = Array.from({ length: 50 }, (_, i) => msg({ id: i + 1, text: "m" + i }))
    vi.mocked(api).mockImplementation(
      async (_m: string, p: string) => (p === "/api/sessions/s2" ? info : pg(full)) as any,
    )
    await mk()
    expect(btn("Load older")).toBeNull()
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
    const user = userEvent.setup()
    const { c } = await mk()
    await user.click(btn("Load older")!)
    await flushPromises()
    expect(c.querySelectorAll('[data-message-id="10"]')).toHaveLength(1)
    expect(within(item(c, 9)).getByText("older")).toBeTruthy()
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
    const user = userEvent.setup()
    const { c } = await mk()
    await user.click(btn("Load newer")!)
    await flushPromises()
    expect(api).toHaveBeenCalledWith("GET", "/api/sessions/s2/messages?after=80&limit=50")
    expect(within(item(c, 90)).getByText("a newer question")).toBeTruthy()
    expect(btn("Load newer")).toBeNull()
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
    const user = userEvent.setup()
    const { c, router } = await mk()
    await user.click(btn("Load newer")!)
    await router.push("/sessions/s3")
    await flushPromises()
    release(pg([msg({ id: 90, text: "a newer question" })]))
    await flushPromises()
    expect(c.textContent).toContain("other session")
    expect(c.textContent).not.toContain("a newer question")
  })

  it("marks the hit and jumps to the latest messages", async () => {
    const user = userEvent.setup()
    const { c, router } = await mk("/sessions/s2?message=11")
    expect(item(c, 11).dataset.hit).toBe("true")
    expect(item(c, 11).textContent).toContain("Search hit")
    expect(item(c, 10).dataset.hit).toBeUndefined()
    await user.click(btn("Jump to latest")!)
    await flushPromises()
    expect(router.currentRoute.value.query).toEqual({})
    expect(api).toHaveBeenLastCalledWith("GET", "/api/sessions/s2/messages?limit=50")
  })

  it("shows the time of each message", async () => {
    const { c } = await mk()
    expect(item(c, 11).querySelector("time")!.getAttribute("datetime")).toBe("2026-11-20T09:05:00Z")
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
    await mk()
    expect(api).toHaveBeenCalledWith("GET", "/api/sessions/p1")
    expect(screen.getByRole("link", { name: "subagent run" }).getAttribute("href")).toBe(
      "/sessions/c1",
    )
    const parent = screen.getByRole("link", { name: "child of Release notes" })
    expect(parent.getAttribute("href")).toBe("/sessions/p1")
    expect(parent.querySelector("svg.lucide-corner-down-right")).not.toBeNull()
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
    const user = userEvent.setup()
    await mk()
    expect(document.querySelector("pre")).toBeNull()
    await user.click(screen.getByRole("button", { name: /patch/ }))
    await flushPromises()
    expect(api).toHaveBeenCalledWith("GET", "/api/sessions/s2/tools/c1")
    expect(document.querySelector("pre")!.textContent).toContain('"path": "internal/cron/jobs.go"')
    expect(screen.getByTestId("tool-result")).toBeTruthy()
    // A second click closes the tool line.
    await user.click(screen.getByRole("button", { name: /patch/ }))
    await flushPromises()
    expect(document.querySelector("pre")).toBeNull()
  })

  it("shows the empty text for a session with no messages", async () => {
    vi.mocked(api).mockImplementation(
      async (_m: string, p: string) => (p === "/api/sessions/s2" ? info : pg([])) as any,
    )
    await mk()
    expect(screen.getByText("No messages yet.")).toBeTruthy()
    expect(document.querySelector('[data-slot="empty"]')).not.toBeNull()
    expect(screen.queryByRole("region", { name: "Messages" })).toBeNull()
    expect(document.querySelector('[data-slot="message-scroller"]')).toBeNull()
  })

  it("loads again from the action of the error state", async () => {
    vi.mocked(api).mockRejectedValueOnce(new Error("HTTP 500"))
    const user = userEvent.setup()
    const { c } = await mk()
    await user.click(screen.getByRole("button", { name: "Retry" }))
    await flushPromises()
    expect(c.textContent).toContain("Photos")
  })

  it("goes to Chat only when the fork reply is ok", async () => {
    vi.mocked(runCommand).mockResolvedValueOnce({
      request_id: "r",
      text: "⏳ **Busy**\n`/fork` cannot run during a task. Wait for the answer, or stop the task.",
      buttons: [[{ text: "⏹ Stop the task", data: "/stop" }]],
      edit: false,
    })
    const user = userEvent.setup()
    const { router } = await mk()
    await user.click(btn("Fork into dashboard")!)
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
      const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
      await mk()
      await user.click(btn("Export JSONL")!)
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
    const { c } = await mk()
    const t = item(c, 11).querySelector("time")!
    expect(t.textContent?.trim()).toBe(shortTime("2026-11-20T09:05:00Z"))
    expect(t.getAttribute("title")).toBe(fullTime("2026-11-20T09:05:00Z"))
    expect(t.getAttribute("datetime")).toBe("2026-11-20T09:05:00Z")
    const started = screen.getByText(shortTime("2026-11-19T09:00:00Z"))
    expect(started.getAttribute("title")).toBe(fullTime("2026-11-19T09:00:00Z"))
    expect(started.classList.contains("whitespace-nowrap")).toBe(true)
  })

  it("stacks the parts of each message in one column", async () => {
    const { c } = await mk()
    const assistant = item(c, 12).querySelectorAll('[data-slot="message-content"]')
    expect(assistant).toHaveLength(1)
    const col = within(assistant[0] as HTMLElement)
    expect(col.getByText("Tether")).toBeTruthy()
    expect(col.getByRole("button", { name: /Reasoning\s*812 tokens/ })).toBeTruthy()
    expect(col.getByText("Runs() had no offset.")).toBeTruthy()
    expect(assistant[0].querySelector("time")).not.toBeNull()
    expect(assistant[0].textContent).toContain("$0.0061+")
    expect(assistant[0].textContent).toContain("655 out")

    const user = item(c, 11).querySelectorAll('[data-slot="message-content"]')
    expect(user).toHaveLength(1)
    const ucol = within(user[0] as HTMLElement)
    expect(ucol.getByText("You")).toBeTruthy()
    expect(ucol.getByText("Find the flaky test.")).toBeTruthy()
    expect(user[0].querySelector("time")).not.toBeNull()
  })

  it("links back to the session list with its last query", async () => {
    rememberListQuery("sessions", { src: "cli", page: "2" })
    try {
      await mk()
      expect(screen.getByRole("link", { name: "Sessions" }).getAttribute("href")).toBe(
        "/sessions?src=cli&page=2",
      )
      expect(
        screen.getByRole("link", { name: "Sessions" }).querySelector("svg.lucide-chevron-left"),
      ).not.toBeNull()
    } finally {
      rememberListQuery("sessions", {})
    }
  })
})
