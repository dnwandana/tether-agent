import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { flushPromises } from "@vue/test-utils"
import { render, screen, within } from "@testing-library/vue"

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
  const r = render(ModelsPage, { global: { stubs: { LiveHeader: true } } })
  await flushPromises()
  return r
}
const entries = () => screen.getAllByRole("table")[0]
const bodyRows = (t: HTMLElement) => within(t).getAllByRole("row").slice(1)
const cells = (row: HTMLElement) => within(row).getAllByRole("cell")
const tone = (el: Element | null) => el?.querySelector("[data-tone]")?.getAttribute("data-tone")

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
    const { unmount } = await mk()
    expect(api).toHaveBeenCalledWith("GET", "/api/models")
    const rows = bodyRows(entries())
    expect(rows.map((r) => cells(r)[7].textContent?.trim())).toEqual([
      "⚠ skipped for 42 s · HTTP 503 at 14:02",
      "✓ answers now",
      "✗ not usable: no key",
    ])
    expect(rows.map((r) => tone(cells(r)[7]))).toEqual(["warn", "ok", "bad"])
    vi.advanceTimersByTime(1000)
    await flushPromises()
    expect(cells(bodyRows(entries())[0])[7].textContent?.trim()).toBe(
      "⚠ skipped for 41 s · HTTP 503 at 14:02",
    )
    unmount()
  })

  it("shows the fallback banner and the roles", async () => {
    const { unmount } = await mk()
    expect(screen.getByRole("alert").textContent?.trim()).toBe(
      "⚠ Fallback active. backup (deepseek-chat) answers. primary failed at 14:02: HTTP 503.",
    )
    expect(screen.getByText("goals.judge_provider")).toBeTruthy()
    expect(screen.getByText("128k")).toBeTruthy()
    unmount()
  })

  it("keeps the data and shows a toast when a live reload fails", async () => {
    const { unmount } = await mk()
    expect(screen.getByRole("alert")).toBeTruthy()
    vi.mocked(api).mockRejectedValue(new Error("boom"))
    await h.live!.load()
    await flushPromises()
    expect(screen.getByRole("alert")).toBeTruthy()
    expect(toast).toHaveBeenCalledWith("boom", "error")
    unmount()
  })

  it("shows no banner when the primary answers", async () => {
    vi.mocked(api).mockResolvedValue({ ...view, fallback: { ...view.fallback, active: false } })
    const { unmount } = await mk()
    expect(screen.queryByRole("alert")).toBeNull()
    unmount()
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
    const { unmount } = await mk()
    const steps = within(screen.getByRole("list", { name: "Fallback chain" })).getAllByRole(
      "listitem",
    )
    expect(steps).toHaveLength(2)
    expect(within(steps[0]).getByText("Primary")).toBeTruthy()
    expect(within(steps[1]).getByText("Fallback 1")).toBeTruthy()
    expect(steps.map((s) => s.querySelector("[data-tone]")?.textContent?.trim())).toEqual([
      "⚠ skipped",
      "✓ answers",
    ])
    expect(steps[0].textContent?.match(/primary/gi)).toHaveLength(1)
    expect(within(steps[1]).getByText("backup")).toBeTruthy()
    expect(tone(steps[0])).toBe("warn")
    unmount()
  })

  it("merges preset and format and wraps the model and the base URL", async () => {
    const { unmount } = await mk()
    expect(
      within(entries())
        .getAllByRole("columnheader")
        .map((c) => c.textContent?.trim()),
    ).toEqual([
      "Entry",
      "Preset · format",
      "Model",
      "Base URL",
      "Vision",
      "Context",
      "Effort",
      "Status",
    ])
    const tds = cells(bodyRows(entries())[0])
    expect(tds[1].textContent?.trim()).toBe("zai · openai")
    expect(tds[2].querySelector(".font-mono.wrap-anywhere")?.textContent?.trim()).toBe("glm-4.6")
    expect(tds[3].querySelector(".font-mono.wrap-anywhere")?.textContent?.trim()).toBe(
      "https://api.z.ai/api/paas/v4",
    )
    unmount()
  })

  it("marks an entry that no chain step and no role uses", async () => {
    const { unmount } = await mk()
    const rows = bodyRows(entries())
    expect(rows.map((r) => within(cells(r)[0]).queryByText("not used") !== null)).toEqual([
      false,
      false,
      true,
    ])
    expect(cells(rows[2])[0].textContent?.replace(/\s+/g, " ").trim()).toBe("cheap not used")
    unmount()
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
    const { unmount } = await mk()
    expect(bodyRows(entries()).map((r) => cells(r)[5].textContent?.trim())).toEqual([
      "200k",
      "128k",
      "1M",
    ])
    unmount()
  })

  it("shows the last failure of a failed entry", async () => {
    vi.mocked(api).mockResolvedValue({
      ...view,
      entries: [
        entry({ name: "backup", status: "failed", cause: "HTTP 500", failed_at: local(13, 58) }),
      ],
    })
    const { unmount } = await mk()
    const c = cells(bodyRows(entries())[0])[7]
    expect(c.textContent?.trim()).toBe("⚠ last failed 13:58 · HTTP 500")
    expect(tone(c)).toBe("warn")
    unmount()
  })

  it("shows the base URL of each entry and an unusable role entry", async () => {
    vi.mocked(api).mockResolvedValue({
      ...view,
      roles: [
        { role: "goal_judge", key: "goals.judge_provider", value: "cheap", entry_usable: false },
      ],
    })
    const { unmount } = await mk()
    expect(screen.getByText("https://api.z.ai/api/paas/v4")).toBeTruthy()
    expect(screen.getByText("⚠️ entry unusable").getAttribute("data-tone")).toBe("warn")
    unmount()
  })

  it("loads again when the skip time ends", async () => {
    const { unmount } = await mk()
    expect(api).toHaveBeenCalledTimes(1)
    vi.advanceTimersByTime(43000)
    await flushPromises()
    expect(api).toHaveBeenCalledTimes(2)
    unmount()
  })

  it("reloads on a status event only when the fallback or the entry changes", async () => {
    status.fallback = false
    status.entry = "primary"
    const { unmount } = await mk()
    expect(h.live!.opts!.events).toEqual(["status"])
    const f = h.live!.opts!.filter!
    expect(f("status", { spend24h: 1.5 })).toBe(false)
    expect(f("status", { fallback: true })).toBe(true)
    expect(f("status", { fallback: true })).toBe(false)
    expect(f("status", { entry: "backup" })).toBe(true)
    unmount()
  })

  it("shows the empty text with the setup command", async () => {
    vi.mocked(api).mockResolvedValue({ ...view, entries: [] })
    const { unmount } = await mk()
    expect(screen.getByText("No model entries. Run `tether setup` on the host.")).toBeTruthy()
    unmount()
  })

  it("pages the entries and the roles with their own stored sizes", async () => {
    localStorage.setItem("tether.rows.models", "10")
    localStorage.setItem("tether.rows.model-roles", "10")
    vi.mocked(api).mockResolvedValue({
      ...view,
      entries: [
        ...view.entries,
        ...Array.from({ length: 9 }, (_, i) => entry({ name: `extra${i + 1}` })),
      ],
      roles: [
        ...view.roles,
        ...Array.from({ length: 10 }, (_, i) => ({
          role: `role${i + 1}`,
          key: `key${i + 1}`,
          value: "",
          entry_usable: true,
        })),
      ],
    })
    const { unmount } = await mk()
    expect(screen.getByRole("navigation", { name: "Model entries pages" })).toBeTruthy()
    expect(screen.getByRole("navigation", { name: "Model roles pages" })).toBeTruthy()
    expect(screen.getByText("1–10 of 12")).toBeTruthy()
    expect(screen.getByText("1–10 of 11")).toBeTruthy()
    expect(bodyRows(entries())).toHaveLength(10)
    expect(cells(bodyRows(entries())[0])[2].classList).toContain("whitespace-normal")
    unmount()
  })
})
