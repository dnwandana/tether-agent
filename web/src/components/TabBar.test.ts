import { mount } from "@vue/test-utils"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { createMemoryHistory, createRouter } from "vue-router"

const names = [
  "chat",
  "sessions",
  "transcript",
  "jobs",
  "job",
  "goals",
  "goal",
  "security",
  "skills",
  "skill",
  "memory",
  "usage",
  "models",
  "system",
  "ops",
  "access",
  "start",
  "signin",
]
const paths: Record<string, string> = {
  transcript: "/sessions/:id",
  job: "/jobs/:id",
  goal: "/goals/:session",
  skill: "/skills/:name",
  ops: "/system/ops",
}
function makeRouter() {
  return createRouter({
    history: createMemoryHistory(),
    routes: names.map((name) => ({
      name,
      path: paths[name] ?? "/" + name,
      component: { render: () => null },
    })),
  })
}

vi.mock("../api/client", () => ({ api: vi.fn(), ApiError: class extends Error {} }))
import TabBar from "./TabBar.vue"
import { ICON } from "../lib/icons"
import { cards } from "../stores/cards"
import { prefs, ui } from "../stores/ui"

async function at(path: string) {
  const router = makeRouter()
  router.push(path)
  await router.isReady()
  return mount(TabBar, {
    attachTo: document.body,
    global: { plugins: [router], stubs: { WaitingTray: true } },
  })
}

describe("TabBar", () => {
  beforeEach(() => {
    ui.trayOpen = false
  })
  afterEach(() => {
    document.body.innerHTML = ""
  })

  it("has four tabs, the waiting badge, and aria-current on the active tab", async () => {
    cards.open = [{ id: "a1" }, { id: "a2" }, { id: "a3" }] as never
    const w = await at("/chat")
    expect(w.findAll(".tab").map((t) => t.find("span").text())).toEqual([
      "Chat",
      "Jobs",
      "Waiting",
      "More",
    ])
    expect(w.findAll(".tab")[2].find("path").attributes("d")).toBe(ICON.waiting)
    expect(w.findAll(".tab")[2].find(".badge").text()).toBe("3")
    expect(w.findAll(".tab")[0].classes()).toContain("is-on")
    expect(w.findAll(".tab")[0].attributes("aria-current")).toBe("page")
    expect(w.findAll(".tab")[1].attributes("aria-current")).toBeUndefined()
  })

  it("opens the tray from Waiting and shows Waiting as selected while it is open", async () => {
    const w = await at("/chat")
    await w.findAll(".tab")[2].trigger("click")
    expect(ui.trayOpen).toBe(true)
    expect(w.findAll(".tab")[2].classes()).toContain("is-on")
    expect(w.findAll(".tab")[2].attributes("aria-expanded")).toBe("true")
    expect(w.findAll(".tab")[0].classes()).not.toContain("is-on")
    ui.trayOpen = false
    await w.vm.$nextTick()
    expect(w.findAll(".tab")[2].classes()).not.toContain("is-on")
  })

  it("groups the More sheet and marks the current page", async () => {
    const w = await at("/usage")
    expect(w.findAll(".tab")[3].classes()).toContain("is-on")
    await w.findAll(".tab")[3].trigger("click")
    const sheet = w.find(".ov .dialog")
    expect(sheet.findAll(".eyebrow").map((x) => x.text())).toEqual(["Work", "Agent", "Host"])
    const items = sheet.findAll(".li").map((x) => x.text())
    expect(items).toEqual([
      "⌘ Commands and skills",
      "Sessions",
      "Goals",
      "Approvals and security",
      "Skills",
      "Memory",
      "Models",
      "Usage",
      "System",
      "System operations",
      "Access",
      "Getting started",
      "Theme: System ▸",
    ])
    const usage = sheet.findAll(".li").find((x) => x.text() === "Usage")!
    expect(usage.attributes("aria-current")).toBe("page")
    expect(usage.classes()).toContain("is-on")
  })

  it("marks System operations, not System, on the ops page", async () => {
    const w = await at("/system/ops")
    await w.findAll(".tab")[3].trigger("click")
    const on = w.findAll(".ov .li[aria-current='page']").map((x) => x.text())
    expect(on).toEqual(["System operations"])
  })

  it("sets the theme from the More sheet", async () => {
    prefs.theme = "system"
    const w = await at("/chat")
    await w.findAll(".tab")[3].trigger("click")
    await w
      .findAll(".ov .li")
      .find((x) => x.text() === "Theme: System ▸")!
      .trigger("click")
    expect(w.findAll(".ov .theme-choice").map((x) => x.text())).toEqual([
      "✓ System",
      "🌙 Dark",
      "☀️ Light",
    ])
    await w.findAll(".ov .theme-choice")[2].trigger("click")
    expect(prefs.theme).toBe("light")
    expect(w.find(".ov").exists()).toBe(false)
  })

  it("closes the More sheet on Esc", async () => {
    const w = await at("/chat")
    await w.findAll(".tab")[3].trigger("click")
    document.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }),
    )
    await w.vm.$nextTick()
    expect(w.find(".ov").exists()).toBe(false)
  })

  it("hides the badge when nothing waits", async () => {
    cards.open = []
    const w = await at("/chat")
    expect(w.findAll(".tab")[2].find(".badge").exists()).toBe(false)
  })
})
