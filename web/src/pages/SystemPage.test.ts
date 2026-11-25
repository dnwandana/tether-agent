import { describe, it, expect, vi, beforeEach } from "vitest"
import { defineComponent, h as vh, type PropType } from "vue"
import { render, screen, within } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import { flushPromises } from "@vue/test-utils"
import { createRouter, createMemoryHistory } from "vue-router"
import type { ResultNoteData } from "../lib/command"

const h = vi.hoisted(() => ({
  follow: new Map<string, (t: unknown) => void>(),
  run: null as any,
  live: null as null | {
    load: () => Promise<void>
    opts?: { events?: string[]; filter?: (n: string, d: unknown) => boolean }
  },
}))
vi.mock("../api/client", () => ({
  api: vi.fn(),
  runCommand: vi.fn(),
  ApiError: class ApiError extends Error {
    status: number
    constructor(s: number, m: string) {
      super(m)
      this.status = s
    }
  },
}))
vi.mock("../stores/ui", async () => ({
  ...(await vi.importActual<object>("../stores/ui")),
  toast: vi.fn(),
  confirm: vi.fn(),
}))
vi.mock("../lib/errors", () => ({
  friendlyError: (e: unknown) => (e instanceof Error ? e.message : String(e)),
}))
vi.mock("../lib/command", async () => {
  const actual = await vi.importActual<object>("../lib/command")
  const { ref } = await import("vue")
  h.run = vi.fn()
  return {
    ...actual,
    useCommand: () => ({ run: h.run, reply: ref(null), closeReply: () => {} }),
  }
})
vi.mock("../lib/live", async () => {
  const actual = await vi.importActual<object>("../lib/live")
  const { onMounted, ref } = await import("vue")
  return {
    ...actual,
    useLiveData: (load: () => Promise<void>, opts?: any) => {
      h.live = { load, opts }
      onMounted(() => void load())
      return { stale: ref(false), updatedAt: ref(null), refresh: load }
    },
    followTask: (id: string, fn: (t: unknown) => void) => {
      h.follow.set(id, fn)
      return () => h.follow.delete(id)
    },
  }
})
import { api, ApiError } from "../api/client"
import { confirm, toast } from "../stores/ui"
import SystemPage from "./SystemPage.vue"
import { shortTime } from "../lib/time"

const Blank = { render: () => null }
const local = (hh: number, mm: number) => new Date(2026, 10, 20, hh, mm).toISOString()
const mcpRow = (o: object) => ({
  name: "",
  transport: "stdio",
  connected: false,
  tools: 0,
  classes: {},
  last_error: "",
  last_error_at: "",
  tool_names: [],
  always_on: false,
  idle_stop_minutes: 10,
  include: [],
  exclude: [],
  enabled: true,
  ...o,
})
const proc = (pid: number) => ({
  pid,
  session_id: "s",
  command: `job-${pid}`,
  started: local(14, 2),
  ended: "",
  exited: false,
  exit_code: 0,
  tail: "",
})
let data: Record<string, unknown>
const reset = () => {
  data = {
    "/api/doctor": {
      ran_at: local(14, 2),
      task_id: "",
      checks: [
        { status: "ok", name: "config", detail: "loaded; 3 provider entries", next: "" },
        {
          status: "fail",
          name: "mcp linear",
          detail: "cannot start: npx not found",
          next: "Open the Models page to see the entries.",
        },
      ],
    },
    "/api/mcp": [
      mcpRow({
        name: "github",
        connected: true,
        tools: 2,
        classes: { read: 1, write: 1 },
        tool_names: ["get_issue", "delete_repo"],
        always_on: true,
        include: ["get_issue"],
        exclude: ["delete_repo"],
      }),
      mcpRow({ name: "linear", last_error: "npx: not found", last_error_at: local(13, 40) }),
      mcpRow({ name: "notion", enabled: false }),
    ],
    "/api/runtime": {
      batches: [
        {
          id: "b1",
          key: "dashboard",
          status: "running",
          tasks: [{ name: "scan", goal: "g" }],
          results: [
            {
              status: "failed",
              summary: "one\ntwo\nthree\nfour",
              error: "timeout",
              tool_calls: 4,
              duration_s: 12,
            },
          ],
          created: local(14, 0),
        },
      ],
      acp: [{ session_id: "s1", agent: "claude-code", cwd: "~/projects/ledger", busy: true }],
      processes: [proc(48211)],
      browser: false,
      running: 1,
    },
  }
}

// A stub that shows the note data in an output element, so that tests read the exact data.
const NoteStub = defineComponent({
  props: { note: { type: Object as PropType<ResultNoteData | null>, default: null } },
  setup: (p) => () =>
    p.note
      ? vh("output", { "data-title": p.note.title, "data-kind": p.note.kind }, p.note.text)
      : null,
})
const notes = () =>
  [...document.querySelectorAll("output")].map((o) => ({
    title: (o as HTMLElement).dataset.title,
    kind: (o as HTMLElement).dataset.kind,
    text: o.textContent,
  }))

async function mk() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/system", name: "system", component: SystemPage },
      { path: "/system/ops", name: "ops", component: Blank },
      { path: "/models", name: "models", component: Blank },
    ],
  })
  await router.push("/system")
  const r = render(SystemPage, {
    global: { plugins: [router], stubs: { LiveHeader: true, ResultNote: NoteStub } },
  })
  await flushPromises()
  return Object.assign(r, { router, user: userEvent.setup() })
}
const mcp = () => screen.getByRole("region", { name: "MCP servers" })
const button = (name: string | RegExp) => screen.getByRole("button", { name })
const bodyRows = () => [...mcp().querySelectorAll("tbody tr")] as HTMLElement[]
const task = (o: object) => ({
  id: "t1",
  kind: "doctor",
  state: "running",
  text: "",
  subject: "",
  code: "",
  waiting: false,
  ...o,
})

beforeEach(() => {
  reset()
  h.follow.clear()
  h.live = null
  h.run.mockReset().mockResolvedValue("info")
  window.matchMedia = ((q: string) => ({
    matches: false,
    media: q,
    addEventListener() {},
    removeEventListener() {},
  })) as any
  vi.mocked(api)
    .mockReset()
    .mockImplementation(
      async (m: string, p: string) => (m === "GET" ? data[p] : { task_id: "t1" }) as any,
    )
  vi.mocked(confirm).mockReset().mockResolvedValue(true)
  vi.mocked(toast).mockReset()
})

// Returns the fourth cell of the MCP row that names the server.
const cell3 = (name: string) =>
  bodyRows()
    .find((r) => r.textContent?.includes(name))!
    .querySelectorAll("td")[3] as HTMLElement
const summary = () => screen.getByText(/^one/).closest(".typeset")!
const items = () =>
  within(screen.getByRole("list", { name: "Background processes" })).getAllByRole("listitem")

describe("SystemPage", () => {
  it("shows the doctor, the MCP servers, and the running work", async () => {
    await mk()
    for (const p of ["/api/doctor", "/api/mcp", "/api/runtime"])
      expect(api).toHaveBeenCalledWith("GET", p)
    expect(screen.getByText("1 read · 1 write")).toBeTruthy()
    expect(screen.getByText("job-48211")).toBeTruthy()
    expect(screen.getByText("claude-code")).toBeTruthy()
    expect(document.body.textContent).toContain("npx: not found")
  })

  it("loads each section alone, with its own Retry", async () => {
    vi.mocked(api).mockImplementation(async (m: string, p: string) => {
      if (p === "/api/mcp") throw new Error("mcp read failed")
      return data[p] as any
    })
    const { user } = await mk()
    const text = document.body.textContent
    expect(text).toContain("mcp read failed")
    expect(text).toContain("config")
    expect(text).toContain("claude-code")
    vi.mocked(api).mockImplementation(async (m: string, p: string) => data[p] as any)
    await user.click(within(mcp()).getByRole("button", { name: "Retry" }))
    await flushPromises()
    const name = (data["/api/mcp"] as any[])[0].name
    expect(within(mcp()).getByText(name)).toBeTruthy()
  })

  it("gives each MCP dot the class of its state and shows the settings", async () => {
    await mk()
    const tones = bodyRows().map((r) => r.querySelector("[data-tone]")?.getAttribute("data-tone"))
    expect(tones).toEqual(["ok", "bad", "off"])
    const text = mcp().textContent
    for (const s of [
      "always on",
      "stops after 10 min idle",
      "include: get_issue",
      "exclude: delete_repo",
      "off",
      "2 tools",
    ])
      expect(text).toContain(s)
  })

  it("shows the MCP empty state with a short title", async () => {
    data["/api/mcp"] = []
    await mk()
    expect(within(mcp()).getByText("No MCP servers.")).toBeTruthy()
    expect(
      within(mcp()).getByText("Add one under `mcp` in config.yaml, then tap Reload MCP."),
    ).toBeTruthy()
  })

  it("shows only N tools in the Tools cell, with 1 tool for one", async () => {
    ;(data["/api/mcp"] as any[]).push(
      mcpRow({ name: "search", connected: true, tools: 1, tool_names: ["find"] }),
    )
    await mk()
    expect(cell3("github").textContent?.startsWith("2 tools")).toBe(true)
    expect(within(cell3("search")).getByRole("button", { name: "1 tool" })).toBeTruthy()
    expect(cell3("linear").textContent?.trim()).toBe("0 tools")
  })

  it("shows the batch id apart from its state, the duration, and the Markdown summary", async () => {
    const b = (data["/api/runtime"] as any).batches[0]
    b.results[0].duration_s = 125.4
    b.results[0].summary = "**Done.**\n\n- one\n- two\n- three\n- four"
    await mk()
    const text = document.body.textContent
    expect(text).toContain("b1 running · 1 task")
    expect(screen.getByText("running · 1 task")).toBeTruthy()
    expect(text).toContain("scan · failed · 4 tool calls · 2 m 5 s")
    expect(text).not.toContain("125.4")
    const sum = screen.getByText("Done.").closest(".typeset")!
    expect(sum.classList.contains("typeset-chat")).toBe(true)
    expect(sum.classList.contains("line-clamp-3")).toBe(true)
    expect(sum.querySelector("strong")?.textContent).toBe("Done.")
    expect(sum.querySelectorAll("li")).toHaveLength(4)
  })

  it("shows the MCP error time and the process start in the short format", async () => {
    await mk()
    const text = document.body.textContent
    expect(text).toContain(`npx: not found (${shortTime(local(13, 40))})`)
    expect(text).toContain(`started ${shortTime(local(14, 2))}`)
  })

  it("asks before Reload MCP and shows the report in a result note", async () => {
    let done!: (k: string) => void
    h.run.mockImplementation(
      (_t: string, o: any) =>
        new Promise((r) => {
          o.result.value = {
            title: "Reload MCP",
            at: new Date(),
            text: "🔌 MCP reloaded: 3 servers.",
            kind: "info",
          }
          done = r
        }),
    )
    const { user } = await mk()
    await user.click(button("Reload MCP"))
    await flushPromises()
    expect(confirm).toHaveBeenCalledWith({
      title: "Reload the MCP servers?",
      body: "The prompt cache of each session starts again, so the next turn of each session costs more.",
      confirmLabel: "Reload MCP",
    })
    expect(h.run).toHaveBeenCalledWith("/reload-mcp", {
      result: expect.anything(),
      title: "Reload MCP",
    })
    expect((button("Reload MCP") as HTMLButtonElement).disabled).toBe(true)
    vi.mocked(api).mockClear()
    done("info")
    await flushPromises()
    expect((button("Reload MCP") as HTMLButtonElement).disabled).toBe(false)
    expect(api).toHaveBeenCalledWith("GET", "/api/mcp")
    expect(notes()[0]).toMatchObject({ text: "🔌 MCP reloaded: 3 servers." })
  })

  it("does not reload MCP when the owner declines", async () => {
    vi.mocked(confirm).mockResolvedValue(false)
    const { user } = await mk()
    await user.click(button("Reload MCP"))
    await flushPromises()
    expect(h.run).not.toHaveBeenCalled()
  })

  it("shows the counts of the doctor in its header and links a page name", async () => {
    await mk()
    expect(screen.getByText("ran 14:02 · 1 passed · 0 warnings · 1 failed")).toBeTruthy()
    expect(screen.getByRole("link", { name: "the Models page" }).getAttribute("href")).toBe(
      "/models",
    )
  })

  it("marks each doctor check with an icon and a word, and each runtime title with an icon", async () => {
    ;(data["/api/doctor"] as any).checks.push({
      status: "skip",
      name: "browser",
      detail: "not set",
      next: "",
    })
    await mk()
    const checks = within(screen.getByRole("list", { name: "Doctor checks" })).getAllByRole(
      "listitem",
    )
    expect(checks[0].querySelector("svg.lucide-check")!.classList).toContain("text-success")
    expect(within(checks[0]).getByText("passed").classList).toContain("sr-only")
    expect(checks[1].querySelector("svg.lucide-x")!.classList).toContain("text-destructive")
    expect(within(checks[1]).getByText("failed").classList).toContain("sr-only")
    expect(checks[2].querySelector("svg")).toBeNull()
    expect(checks[2].textContent).not.toContain("•")
    for (const [title, icon] of [
      ["Delegation batches", "lucide-git-branch"],
      ["ACP sessions", "lucide-bot"],
      ["Background processes", "lucide-square-terminal"],
      ["Browser", "lucide-globe"],
    ]) {
      const t = screen.getByText(title).closest('[data-slot="card-title"]')!
      expect(t.querySelector(`svg.${icon}`)).not.toBeNull()
    }
  })

  it("shows an empty doctor with Run doctor", async () => {
    data["/api/doctor"] = { ran_at: "0001-01-01T00:00:00Z", task_id: "", checks: [] }
    const { user } = await mk()
    expect(screen.getByText("The doctor has not run yet.")).toBeTruthy()
    await user.click(button("Run doctor"))
    await vi.waitFor(() => expect(api).toHaveBeenCalledWith("POST", "/api/doctor"))
  })

  it("follows the doctor by the task event and dims the old checks", async () => {
    const { user } = await mk()
    await user.click(button("Run again"))
    await flushPromises()
    expect((button(/Running…/) as HTMLButtonElement).disabled).toBe(true)
    expect(screen.getByRole("list", { name: "Doctor checks" }).getAttribute("aria-busy")).toBe(
      "true",
    )
    vi.mocked(api).mockClear()
    h.follow.get("t1")!(task({ state: "done" }))
    await flushPromises()
    expect(api).toHaveBeenCalledWith("GET", "/api/doctor")
    expect((button("Run again") as HTMLButtonElement).disabled).toBe(false)
  })

  it("shows the error and the lost text of a doctor task in a result note", async () => {
    const { user } = await mk()
    await user.click(button("Run again"))
    await flushPromises()
    h.follow.get("t1")!(task({ state: "error", text: "no provider answered" }))
    await flushPromises()
    expect(notes()[0]).toMatchObject({
      title: "Doctor",
      kind: "error",
      text: "no provider answered",
    })
    await user.click(button("Run again"))
    await flushPromises()
    h.follow.get("t1")!(task({ state: "lost" }))
    await flushPromises()
    expect(notes()[0]).toMatchObject({
      text: "The gateway restarted. The result of the task is lost.",
    })
  })

  it("keeps the server text of a refused doctor run", async () => {
    vi.mocked(api).mockImplementation(async (m: string, p: string) => {
      if (m === "POST")
        throw new (ApiError as any)(429, "The doctor ran 12 s ago. Try again in 48 s.")
      return data[p] as any
    })
    const { user } = await mk()
    await user.click(button("Run again"))
    await flushPromises()
    expect(toast).toHaveBeenCalledWith("The doctor ran 12 s ago. Try again in 48 s.", "error")
  })

  it("follows a doctor task that already runs, one time", async () => {
    ;(data["/api/doctor"] as any).task_id = "t7"
    await mk()
    expect(h.follow.has("t7")).toBe(true)
    h.follow.get("t7")!(task({ id: "t7", state: "done" }))
    h.follow.clear()
    await flushPromises()
    expect(h.follow.has("t7")).toBe(false)
  })

  it("lists the batch tasks and cuts the summary at 3 lines", async () => {
    const { user } = await mk()
    const text = document.body.textContent
    expect(text).toContain("scan · failed · 4 tool calls · 12 s")
    expect(text).toContain("timeout")
    expect(summary().classList.contains("line-clamp-3")).toBe(true)
    await user.click(button("Show all"))
    expect(summary().classList.contains("line-clamp-3")).toBe(false)
  })

  it("shows the first 20 processes, then Show all N", async () => {
    ;(data["/api/runtime"] as any).processes = Array.from({ length: 25 }, (_, i) => proc(1000 + i))
    const { user } = await mk()
    expect(items()).toHaveLength(20)
    await user.click(button("Show all 25"))
    expect(items()).toHaveLength(25)
  })

  it("reloads on the end of a doctor task and on a change of the running work", async () => {
    await mk()
    expect(h.live!.opts!.events).toEqual(["task", "status"])
    const f = h.live!.opts!.filter!
    expect(f("task", task({ state: "done" }))).toBe(true)
    expect(f("task", task({ state: "running" }))).toBe(false)
    expect(f("task", task({ kind: "backup", state: "done" }))).toBe(false)
    expect(f("status", { spend24h: 2 })).toBe(false)
    expect(f("status", { running_shell: 2 })).toBe(true)
    expect(f("status", { running_shell: 2 })).toBe(false)
    expect(f("status", { browser_on: true })).toBe(true)
  })

  it("switches to Operations with the tabs", async () => {
    const { user, router } = await mk()
    const tabs = screen.getAllByRole("tab")
    expect(tabs.map((t) => t.textContent?.trim())).toEqual(["Health", "Operations"])
    expect(tabs[0].getAttribute("aria-selected")).toBe("true")
    await user.click(tabs[1])
    await flushPromises()
    expect(router.currentRoute.value.name).toBe("ops")
  })

  it("pages the MCP servers with their own stored size and lets the Tools cell grow", async () => {
    localStorage.setItem("tether.rows.system-mcp", "10")
    data["/api/mcp"] = Array.from({ length: 11 }, (_, i) =>
      mcpRow({ name: `srv${i + 1}`, connected: true, tools: 1, tool_names: ["find"] }),
    )
    const { user } = await mk()
    const nav = within(mcp()).getByRole("navigation", { name: "MCP servers pages" })
    expect(within(mcp()).getByText("1–10 of 11")).toBeTruthy()
    expect(bodyRows()).toHaveLength(10)
    const tools = cell3("srv1")
    expect(tools.classList).toContain("whitespace-normal")
    expect(tools.classList).not.toContain("text-right")
    expect(tools.classList).not.toContain("overflow-hidden")
    await user.click(within(nav).getByRole("button", { name: "Next Page" }))
    expect(within(mcp()).getByText("srv11")).toBeTruthy()
  })
})
