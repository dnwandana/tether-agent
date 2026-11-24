import { describe, it, expect, vi, beforeEach } from "vitest"
import { mount, flushPromises } from "@vue/test-utils"
import { createRouter, createMemoryHistory } from "vue-router"

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
import ResultNote from "../components/ResultNote.vue"
import SystemPage from "./SystemPage.vue"

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
  const w = mount(SystemPage, { global: { plugins: [router], stubs: { LiveHeader: true } } })
  await flushPromises()
  return Object.assign(w, { router })
}
const btn = (w: any, t: string) => w.findAll("button").find((b: any) => b.text() === t)!
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

describe("SystemPage", () => {
  it("shows the doctor, the MCP servers, and the running work", async () => {
    const w = await mk()
    for (const p of ["/api/doctor", "/api/mcp", "/api/runtime"])
      expect(api).toHaveBeenCalledWith("GET", p)
    expect(w.text()).toContain("1 read · 1 write")
    expect(w.text()).toContain("npx: not found")
    expect(w.text()).toContain("job-48211")
    expect(w.text()).toContain("claude-code")
  })

  it("loads each section alone, with its own Retry", async () => {
    vi.mocked(api).mockImplementation(async (m: string, p: string) => {
      if (p === "/api/mcp") throw new Error("mcp read failed")
      return data[p] as any
    })
    const w = await mk()
    expect(w.text()).toContain("mcp read failed")
    expect(w.text()).toContain("config")
    expect(w.text()).toContain("claude-code")
    vi.mocked(api).mockImplementation(async (m: string, p: string) => data[p] as any)
    await w.find(".mcp-section .state-error button").trigger("click")
    await flushPromises()
    expect(w.text()).toContain("github")
  })

  it("gives each MCP dot the class of its state and shows the settings", async () => {
    const w = await mk()
    const dots = w.findAll(".mcp-section .dot").map((d: any) => d.classes())
    expect(dots[0]).toContain("dot-ok")
    expect(dots[1]).toContain("dot-bad")
    expect(dots[2]).toContain("dot-off")
    const text = w.find(".mcp-section").text()
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

  it("shows the MCP empty text", async () => {
    data["/api/mcp"] = []
    const w = await mk()
    expect(w.text()).toContain(
      "No MCP servers. Add one under `mcp` in config.yaml, then tap Reload MCP.",
    )
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
    const w = await mk()
    await btn(w, "Reload MCP").trigger("click")
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
    expect(btn(w, "Reload MCP").attributes("disabled")).toBeDefined()
    vi.mocked(api).mockClear()
    done("info")
    await flushPromises()
    expect(btn(w, "Reload MCP").attributes("disabled")).toBeUndefined()
    expect(api).toHaveBeenCalledWith("GET", "/api/mcp")
    const notes = w
      .findAllComponents(ResultNote)
      .map((n: any) => n.props("note"))
      .filter(Boolean)
    expect(notes[0]).toMatchObject({ text: "🔌 MCP reloaded: 3 servers." })
  })

  it("does not reload MCP when the owner declines", async () => {
    vi.mocked(confirm).mockResolvedValue(false)
    const w = await mk()
    await btn(w, "Reload MCP").trigger("click")
    await flushPromises()
    expect(h.run).not.toHaveBeenCalled()
  })

  it("shows the counts of the doctor in its header and links a page name", async () => {
    const w = await mk()
    expect(w.find(".doctor-card .card-h").text()).toContain("ran 14:02 · 1 ✓ · 0 ⚠ · 1 ✗")
    expect(w.find('.doctor-card a[href="/models"]').text()).toBe("the Models page")
  })

  it("shows an empty doctor with Run doctor", async () => {
    data["/api/doctor"] = { ran_at: "0001-01-01T00:00:00Z", task_id: "", checks: [] }
    const w = await mk()
    expect(w.text()).toContain("The doctor has not run yet.")
    await btn(w, "Run doctor").trigger("click")
    await vi.waitFor(() => expect(api).toHaveBeenCalledWith("POST", "/api/doctor"))
  })

  it("follows the doctor by the task event and dims the old checks", async () => {
    const w = await mk()
    await btn(w, "Run again").trigger("click")
    await flushPromises()
    expect(btn(w, "Running…").attributes("disabled")).toBeDefined()
    expect(w.find(".doctor-card .checks").classes()).toContain("is-stale")
    vi.mocked(api).mockClear()
    h.follow.get("t1")!(task({ state: "done" }))
    await flushPromises()
    expect(api).toHaveBeenCalledWith("GET", "/api/doctor")
    expect(btn(w, "Run again").attributes("disabled")).toBeUndefined()
  })

  it("shows the error and the lost text of a doctor task in a result note", async () => {
    const w = await mk()
    await btn(w, "Run again").trigger("click")
    await flushPromises()
    h.follow.get("t1")!(task({ state: "error", text: "no provider answered" }))
    await flushPromises()
    const note = () =>
      w
        .findAllComponents(ResultNote)
        .map((n: any) => n.props("note"))
        .filter(Boolean)[0]
    expect(note()).toMatchObject({ title: "Doctor", kind: "error", text: "no provider answered" })
    await btn(w, "Run again").trigger("click")
    await flushPromises()
    h.follow.get("t1")!(task({ state: "lost" }))
    await flushPromises()
    expect(note()).toMatchObject({ text: "The gateway restarted. The result of the task is lost." })
  })

  it("keeps the server text of a refused doctor run", async () => {
    vi.mocked(api).mockImplementation(async (m: string, p: string) => {
      if (m === "POST")
        throw new (ApiError as any)(429, "The doctor ran 12 s ago. Try again in 48 s.")
      return data[p] as any
    })
    const w = await mk()
    await btn(w, "Run again").trigger("click")
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
    const w = await mk()
    expect(w.text()).toContain("scan · failed · 4 tool calls · 12 s")
    expect(w.text()).toContain("timeout")
    expect(w.find(".batch-summary").classes()).toContain("clamp-3")
    await btn(w, "Show all").trigger("click")
    expect(w.find(".batch-summary").classes()).not.toContain("clamp-3")
  })

  it("shows the first 20 processes, then Show all N", async () => {
    ;(data["/api/runtime"] as any).processes = Array.from({ length: 25 }, (_, i) => proc(1000 + i))
    const w = await mk()
    expect(w.findAll(".proc-row")).toHaveLength(20)
    await btn(w, "Show all 25").trigger("click")
    expect(w.findAll(".proc-row")).toHaveLength(25)
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
    const w = await mk()
    const tabs = w.findAll('[role="tab"]')
    expect(tabs.map((t: any) => t.text())).toEqual(["Health", "Operations"])
    expect(tabs[0].attributes("aria-selected")).toBe("true")
    await tabs[1].trigger("click")
    await flushPromises()
    expect(w.router.currentRoute.value.name).toBe("ops")
  })
})
