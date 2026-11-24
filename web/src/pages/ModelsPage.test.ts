import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { mount, flushPromises } from "@vue/test-utils"

const h = vi.hoisted(() => ({
  live: null as null | {
    load: () => Promise<void>
    opts?: { events?: string[]; filter?: (n: string, d: unknown) => boolean }
  },
}))
vi.mock("../api/client", () => ({
  api: vi.fn(),
  runCommand: vi.fn(),
  ApiError: class extends Error {},
}))
vi.mock("../stores/ui", async () => ({
  ...(await vi.importActual<object>("../stores/ui")),
  toast: vi.fn(),
}))
vi.mock("../lib/errors", () => ({
  friendlyError: (e: unknown) => (e instanceof Error ? e.message : String(e)),
}))
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
  }
})
import { api } from "../api/client"
import { toast } from "../stores/ui"
import { status } from "../stores/status"
import ModelsPage from "./ModelsPage.vue"

const mk = async () => {
  const w = mount(ModelsPage, { global: { stubs: { LiveHeader: true } } })
  await flushPromises()
  return w
}

const local = (hr: number, m: number, s = 0) => new Date(2026, 10, 20, hr, m, s).toISOString()
const zero = "0001-01-01T00:00:00Z"
const entry = (o: object) => ({
  name: "",
  preset: "zai",
  format: "openai",
  model: "glm-4.6",
  vision: false,
  context_window: 200000,
  effort: "",
  base_url: "",
  status: "ok",
  cause: "",
  failed_at: zero,
  skip_until: zero,
  ...o,
})
const view = {
  fallback: {
    active: true,
    first_name: "primary",
    primary_model: "glm-4.6",
    fallback_name: "backup",
    fallback_model: "deepseek-chat",
    failed_at: local(14, 2),
    cause: "HTTP 503",
  },
  entries: [
    entry({
      name: "primary",
      base_url: "https://api.z.ai/api/paas/v4",
      status: "skipped",
      cause: "HTTP 503",
      failed_at: local(14, 2),
      skip_until: local(14, 3),
    }),
    entry({ name: "backup", preset: "deepseek", model: "deepseek-chat", context_window: 128000 }),
    entry({ name: "cheap", preset: "openrouter", model: "", status: "unusable", cause: "no key" }),
  ],
  chain: ["primary", "backup"],
  health: [],
  roles: [{ role: "goal_judge", key: "goals.judge_provider", value: "backup", entry_usable: true }],
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["Date", "setInterval", "clearInterval"] })
  vi.setSystemTime(new Date(2026, 10, 20, 14, 2, 18))
  window.matchMedia = ((q: string) => ({
    matches: false,
    media: q,
    addEventListener() {},
    removeEventListener() {},
  })) as any
  vi.mocked(api).mockReset().mockResolvedValue(view)
  vi.mocked(toast).mockReset()
})
afterEach(() => vi.useRealTimers())

describe("ModelsPage", () => {
  it("shows the status of each entry from the Health snapshot", async () => {
    const w = mount(ModelsPage, { global: { stubs: { LiveHeader: true } } })
    await flushPromises()
    expect(api).toHaveBeenCalledWith("GET", "/api/models")
    const st = w.findAll(".status")
    expect(st.map((s) => s.text())).toEqual([
      "⚠ skipped for 42 s · HTTP 503 at 14:02",
      "✓ answers now",
      "✗ not usable: no key",
    ])
    expect(st.map((s) => s.classes().find((c) => c.startsWith("c-")))).toEqual([
      "c-warn",
      "c-ok",
      "c-bad",
    ])
    vi.advanceTimersByTime(1000)
    await flushPromises()
    expect(w.find(".status").text()).toBe("⚠ skipped for 41 s · HTTP 503 at 14:02")
    w.unmount()
  })

  it("shows the fallback banner and the roles", async () => {
    const w = mount(ModelsPage, { global: { stubs: { LiveHeader: true } } })
    await flushPromises()
    expect(w.find(".warn-banner").text()).toBe(
      "⚠ Fallback active. backup (deepseek-chat) answers. primary failed at 14:02: HTTP 503.",
    )
    expect(w.text()).toContain("goals.judge_provider")
    expect(w.text()).toContain("128k")
    w.unmount()
  })

  it("keeps the data and shows a toast when a live reload fails", async () => {
    const w = await mk()
    expect(w.find(".warn-banner").exists()).toBe(true)
    vi.mocked(api).mockRejectedValue(new Error("boom"))
    await h.live!.load()
    await flushPromises()
    expect(w.find(".warn-banner").exists()).toBe(true)
    expect(toast).toHaveBeenCalledWith("boom", "error")
    w.unmount()
  })

  it("shows no banner when the primary answers", async () => {
    vi.mocked(api).mockResolvedValue({ ...view, fallback: { ...view.fallback, active: false } })
    const w = mount(ModelsPage, { global: { stubs: { LiveHeader: true } } })
    await flushPromises()
    expect(w.find(".warn-banner").exists()).toBe(false)
    w.unmount()
  })

  it("shows the chain with step-state and no name that repeats the role", async () => {
    vi.mocked(api).mockResolvedValue({
      ...view,
      health: [
        {
          entry: "primary",
          model: "glm-4.6",
          cause: "HTTP 503",
          failed_at: local(14, 2),
          skip_until: local(14, 3),
        },
      ],
    })
    const w = await mk()
    const steps = w.findAll(".chain .chain-step")
    expect(steps.map((s) => [s.find(".role").text(), s.find(".step-state").text()])).toEqual([
      ["Primary", "⚠ skipped"],
      ["Fallback 1", "✓ answers"],
    ])
    expect(steps[0].find(".name").exists()).toBe(false)
    expect(steps[0].text().match(/primary/gi)).toHaveLength(1)
    expect(steps[1].find(".name").text()).toBe("backup")
    expect(steps[0].find(".state").exists()).toBe(false)
    expect(steps[0].find(".step-state").classes()).toContain("c-warn")
    w.unmount()
  })

  it("merges preset and format and wraps the model and the base URL", async () => {
    const w = await mk()
    const table = w.findAll("table")[0]
    expect(table.findAll("th").map((t) => t.text())).toEqual([
      "Entry",
      "Preset · format",
      "Model",
      "Base URL",
      "Vision",
      "Context",
      "Effort",
      "Status",
    ])
    const tds = table.findAll("tbody tr")[0].findAll("td")
    expect(tds[1].text()).toBe("zai · openai")
    expect(tds[2].find(".mono.wrap-any").text()).toBe("glm-4.6")
    expect(tds[3].find(".mono.wrap-any").text()).toBe("https://api.z.ai/api/paas/v4")
    w.unmount()
  })

  it("marks an entry that no chain step and no role uses", async () => {
    const w = await mk()
    const trs = w.findAll("table")[0].findAll("tbody tr")
    const chips = trs.map((tr) =>
      tr
        .findAll("td")[0]
        .findAll(".chip")
        .map((c) => c.text()),
    )
    expect(chips).toEqual([[], [], ["not used"]])
    expect(trs[2].findAll("td")[0].text()).toBe("cheap not used")
    w.unmount()
  })

  it("shows each context window in k or M", async () => {
    vi.mocked(api).mockResolvedValue({
      ...view,
      entries: [
        entry({ name: "primary" }),
        entry({ name: "backup", context_window: 128000 }),
        entry({ name: "big", context_window: 1048576 }),
      ],
    })
    const w = await mk()
    const ctx = w
      .findAll("table")[0]
      .findAll("tbody tr")
      .map((tr) => tr.findAll("td")[5].text())
    expect(ctx).toEqual(["200k", "128k", "1M"])
    w.unmount()
  })

  it("shows the last failure of a failed entry", async () => {
    vi.mocked(api).mockResolvedValue({
      ...view,
      entries: [
        entry({ name: "backup", status: "failed", cause: "HTTP 500", failed_at: local(13, 58) }),
      ],
    })
    const w = await mk()
    expect(w.find(".status").text()).toBe("⚠ last failed 13:58 · HTTP 500")
    expect(w.find(".status").classes()).toContain("c-warn")
    w.unmount()
  })

  it("shows the base URL of each entry and an unusable role entry", async () => {
    vi.mocked(api).mockResolvedValue({
      ...view,
      roles: [
        { role: "goal_judge", key: "goals.judge_provider", value: "cheap", entry_usable: false },
      ],
    })
    const w = await mk()
    expect(w.text()).toContain("https://api.z.ai/api/paas/v4")
    expect(w.text()).toContain("⚠️ entry unusable")
    w.unmount()
  })

  it("loads again when the skip time ends", async () => {
    const w = await mk()
    expect(api).toHaveBeenCalledTimes(1)
    vi.advanceTimersByTime(43000)
    await flushPromises()
    expect(api).toHaveBeenCalledTimes(2)
    w.unmount()
  })

  it("reloads on a status event only when the fallback or the entry changes", async () => {
    status.fallback = false
    status.entry = "primary"
    const w = await mk()
    expect(h.live!.opts!.events).toEqual(["status"])
    const f = h.live!.opts!.filter!
    expect(f("status", { spend24h: 1.5 })).toBe(false)
    expect(f("status", { fallback: true })).toBe(true)
    expect(f("status", { fallback: true })).toBe(false)
    expect(f("status", { entry: "backup" })).toBe(true)
    w.unmount()
  })

  it("shows the empty text with the setup command", async () => {
    vi.mocked(api).mockResolvedValue({ ...view, entries: [] })
    const w = await mk()
    expect(w.text()).toContain("No model entries. Run `tether setup` on the host.")
    w.unmount()
  })
})
