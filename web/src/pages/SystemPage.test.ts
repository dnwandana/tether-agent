import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { mount, flushPromises } from "@vue/test-utils"
import { createRouter, createMemoryHistory } from "vue-router"

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
vi.mock("../stores/ui", () => ({ toast: vi.fn(), confirm: vi.fn() }))
import { api, runCommand, ApiError } from "../api/client"
import { toast } from "../stores/ui"
import SystemPage from "./SystemPage.vue"

const Blank = { render: () => null }
const data: Record<string, unknown> = {
  "/api/doctor": {
    ran_at: "2026-11-20T14:02:00Z",
    task_id: "",
    checks: [
      { status: "ok", name: "config", detail: "loaded; 3 provider entries", next: "" },
      {
        status: "fail",
        name: "mcp linear",
        detail: "cannot start: npx not found",
        next: "install Node, or set mcp.servers.linear.enabled: false",
      },
    ],
  },
  "/api/mcp": [
    {
      name: "github",
      transport: "stdio",
      connected: true,
      tools: 12,
      classes: { read: 8, write: 3, send: 1 },
      last_error: "",
      last_error_at: "",
    },
    {
      name: "linear",
      transport: "stdio",
      connected: false,
      tools: 0,
      classes: {},
      last_error: "npx: not found",
      last_error_at: "2026-11-20T13:40:00Z",
    },
  ],
  "/api/runtime": {
    batches: [],
    acp: [{ session_id: "s1", agent: "claude-code", cwd: "~/projects/ledger", busy: true }],
    processes: [
      {
        pid: 48211,
        session_id: "s",
        command: "rsync -a ~/Pictures nas:/photos",
        started: "2026-11-20T14:02:00Z",
        ended: "",
        exited: false,
        exit_code: 0,
        tail: "sent 1.21G bytes",
      },
    ],
    browser: false,
    running: 1,
  },
}

async function mk() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/system", name: "system", component: SystemPage },
      { path: "/system/ops", name: "ops", component: Blank },
    ],
  })
  await router.push("/system")
  const w = mount(SystemPage, { global: { plugins: [router] } })
  await flushPromises()
  return w
}
const btn = (w: any, t: string) => w.findAll("button").find((b: any) => b.text() === t)!

beforeEach(() => {
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
  vi.mocked(runCommand).mockReset().mockResolvedValue({
    request_id: "r",
    text: "🔌 MCP reloaded: 3 servers.",
    buttons: [],
    edit: false,
  })
  vi.mocked(toast).mockReset()
})
afterEach(() => vi.useRealTimers())

describe("SystemPage", () => {
  it("shows the doctor, the MCP servers, and the running work", async () => {
    const w = await mk()
    for (const p of ["/api/doctor", "/api/mcp", "/api/runtime"])
      expect(api).toHaveBeenCalledWith("GET", p)
    expect(w.text()).toContain("next: install Node, or set mcp.servers.linear.enabled: false")
    expect(w.text()).toContain("8 read · 3 write · 1 send")
    expect(w.text()).toContain("npx: not found")
    expect(w.text()).toContain("rsync -a ~/Pictures nas:/photos")
    expect(w.text()).toContain("claude-code")
  })

  it("shows the limit error of the doctor", async () => {
    vi.mocked(api).mockImplementation(async (m: string, p: string) => {
      if (m === "POST")
        throw new (ApiError as any)(429, "The doctor ran 12 s ago. Try again in 48 s.")
      return data[p] as any
    })
    const w = await mk()
    await btn(w, "Run again").trigger("click")
    await flushPromises()
    expect(api).toHaveBeenCalledWith("POST", "/api/doctor")
    expect(toast).toHaveBeenCalledWith("The doctor ran 12 s ago. Try again in 48 s.", "error")
  })

  it("follows the doctor task until it is done, then reloads the doctor", async () => {
    const w = await mk()
    vi.useFakeTimers({
      toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval", "Date"],
    })
    let st = "running"
    vi.mocked(api).mockImplementation(async (m: string, p: string) => {
      if (m === "POST") return { task_id: "t1" } as any
      if (p === "/api/tasks/t1") return { id: "t1", kind: "doctor", state: st, text: "" } as any
      return data[p] as any
    })
    await btn(w, "Run again").trigger("click")
    await flushPromises()
    expect(btn(w, "Running…").attributes("disabled")).toBeDefined()
    await vi.advanceTimersByTimeAsync(2000)
    expect(api).toHaveBeenCalledWith("GET", "/api/tasks/t1")
    expect(toast).not.toHaveBeenCalled()
    st = "done"
    vi.mocked(api).mockClear()
    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()
    expect(api).toHaveBeenCalledWith("GET", "/api/doctor")
    expect(toast).toHaveBeenCalledWith("Doctor: 1 ✓ · 0 ⚠ · 1 ✗.", "ok")
    expect(btn(w, "Run again")).toBeTruthy()
  })

  it("shows the error text of a failed doctor task", async () => {
    const w = await mk()
    vi.useFakeTimers({
      toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval", "Date"],
    })
    vi.mocked(api).mockImplementation(async (m: string, p: string) => {
      if (m === "POST") return { task_id: "t1" } as any
      if (p === "/api/tasks/t1")
        return { id: "t1", kind: "doctor", state: "error", text: "no provider answered" } as any
      return data[p] as any
    })
    await btn(w, "Run again").trigger("click")
    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()
    expect(toast).toHaveBeenCalledWith("no provider answered", "error")
    expect(btn(w, "Run again").attributes("disabled")).toBeUndefined()
  })

  it("skips a poll tick while the last GET runs and finishes one time", async () => {
    const w = await mk()
    vi.useFakeTimers({
      toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval", "Date"],
    })
    vi.mocked(api).mockImplementation(async (m: string, p: string) => {
      if (m === "POST") return { task_id: "t1" } as any
      if (p === "/api/tasks/t1")
        return new Promise((res) =>
          setTimeout(() => res({ id: "t1", kind: "doctor", state: "done", text: "" }), 3000),
        ) as any
      return data[p] as any
    })
    await btn(w, "Run again").trigger("click")
    await vi.advanceTimersByTimeAsync(10000)
    await flushPromises()
    const polls = vi.mocked(api).mock.calls.filter((c) => c[1] === "/api/tasks/t1")
    expect(polls).toHaveLength(1)
    const done = vi.mocked(toast).mock.calls.filter((c) => String(c[0]).startsWith("Doctor:"))
    expect(done).toHaveLength(1)
  })

  it("shows only the error when the doctor reload fails", async () => {
    const w = await mk()
    vi.useFakeTimers({
      toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval", "Date"],
    })
    vi.mocked(api).mockImplementation(async (m: string, p: string) => {
      if (m === "POST") return { task_id: "t1" } as any
      if (p === "/api/tasks/t1") return { id: "t1", kind: "doctor", state: "done", text: "" } as any
      if (p === "/api/doctor") throw new (ApiError as any)(500, "doctor read failed")
      return data[p] as any
    })
    await btn(w, "Run again").trigger("click")
    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()
    expect(toast).toHaveBeenCalledWith("doctor read failed", "error")
    expect(toast).toHaveBeenCalledTimes(1)
    expect(btn(w, "Run again").attributes("disabled")).toBeUndefined()
  })

  it("stops the poll after 90 s", async () => {
    const w = await mk()
    vi.useFakeTimers({
      toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval", "Date"],
    })
    vi.mocked(api).mockImplementation(async (m: string, p: string) => {
      if (m === "POST") return { task_id: "t1" } as any
      if (p === "/api/tasks/t1")
        return { id: "t1", kind: "doctor", state: "running", text: "" } as any
      return data[p] as any
    })
    await btn(w, "Run again").trigger("click")
    await vi.advanceTimersByTimeAsync(92000)
    expect(toast).toHaveBeenCalledWith(
      "The doctor did not finish in 90 s. Reload the page later.",
      "error",
    )
    vi.mocked(api).mockClear()
    await vi.advanceTimersByTimeAsync(10000)
    expect(api).not.toHaveBeenCalled()
  })

  it("reloads MCP with the command", async () => {
    const w = await mk()
    await btn(w, "Reload MCP").trigger("click")
    await flushPromises()
    expect(runCommand).toHaveBeenCalledWith("/reload-mcp")
    expect(toast).toHaveBeenCalledWith("🔌 MCP reloaded: 3 servers.", "ok")
  })
})
