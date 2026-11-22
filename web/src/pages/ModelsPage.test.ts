import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { mount, flushPromises } from "@vue/test-utils"

vi.mock("../api/client", () => ({
  api: vi.fn(),
  runCommand: vi.fn(),
  ApiError: class extends Error {},
}))
import { api } from "../api/client"
import ModelsPage from "./ModelsPage.vue"

const local = (h: number, m: number, s = 0) => new Date(2026, 10, 20, h, m, s).toISOString()
const zero = "0001-01-01T00:00:00Z"
const entry = (o: object) => ({
  name: "",
  preset: "zai",
  format: "openai",
  model: "glm-4.6",
  vision: false,
  context_window: 200000,
  effort: "",
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
  roles: [{ role: "goal_judge", key: "goals.judge_provider", value: "backup" }],
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
})
afterEach(() => vi.useRealTimers())

describe("ModelsPage", () => {
  it("shows the status of each entry from the Health snapshot", async () => {
    const w = mount(ModelsPage)
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
    const w = mount(ModelsPage)
    await flushPromises()
    expect(w.find(".warn-banner").text()).toBe(
      "⚠ Fallback active. backup (deepseek-chat) answers. primary failed at 14:02: HTTP 503.",
    )
    expect(w.text()).toContain("goals.judge_provider")
    expect(w.text()).toContain("128k")
    w.unmount()
  })

  it("shows no banner when the primary answers", async () => {
    vi.mocked(api).mockResolvedValue({ ...view, fallback: { ...view.fallback, active: false } })
    const w = mount(ModelsPage)
    await flushPromises()
    expect(w.find(".warn-banner").exists()).toBe(false)
    w.unmount()
  })
})
