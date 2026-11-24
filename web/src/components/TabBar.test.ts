import { beforeEach, describe, expect, it, vi } from "vitest"
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
import { render, screen, waitFor, within } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import type { Component } from "vue"
import { nextTick } from "vue"
import TabBar from "./TabBar.vue"
import { ICONS } from "../lib/icons"
import { cards } from "../stores/cards"
import { prefs, ui } from "../stores/ui"

async function at(path: string) {
  const router = makeRouter()
  router.push(path)
  await router.isReady()
  return render(TabBar, { global: { plugins: [router], stubs: { WaitingTray: true } } })
}
const setup = () => userEvent.setup({ pointerEventsCheck: 0 })
const tabs = () => {
  const nav = screen.getByRole("navigation", { name: "Main" })
  return [...nav.querySelectorAll<HTMLElement>(":scope > a, :scope > button")]
}
const sheet = () => screen.getByRole("dialog", { name: "More" })
const items = () => [...sheet().querySelectorAll<HTMLElement>('[data-slot="more-item"]')]
const iconClass = (c: Component) =>
  render(c).container.querySelector("svg")?.getAttribute("class") ?? ""
const svgClass = (el: Element) => el.querySelector("svg")?.getAttribute("class") ?? ""

describe("TabBar", () => {
  beforeEach(() => {
    ui.trayOpen = false
  })

  it("has four tabs, the waiting badge, and aria-current on the active tab", async () => {
    cards.open = [{ id: "a1" }, { id: "a2" }, { id: "a3" }] as never
    await at("/chat")
    expect(tabs().map((t) => t.querySelector("span")?.textContent)).toEqual([
      "Chat",
      "Jobs",
      "Waiting",
      "More",
    ])
    expect(svgClass(tabs()[2])).toBe(iconClass(ICONS.waiting))
    expect(within(tabs()[2]).getByText("3").getAttribute("data-slot")).toBe("badge")
    expect(tabs()[0].hasAttribute("data-active")).toBe(true)
    expect(tabs()[0].getAttribute("aria-current")).toBe("page")
    expect(tabs()[1].getAttribute("aria-current")).toBeNull()
    expect(screen.getByRole("navigation", { name: "Main" }).className).toContain("tab:hidden")
  })

  it("opens the tray from Waiting and shows Waiting as selected while it is open", async () => {
    const user = setup()
    await at("/chat")
    await user.click(tabs()[2])
    expect(ui.trayOpen).toBe(true)
    expect(tabs()[2].hasAttribute("data-active")).toBe(true)
    expect(tabs()[2].getAttribute("aria-expanded")).toBe("true")
    expect(tabs()[0].hasAttribute("data-active")).toBe(false)
    ui.trayOpen = false
    await nextTick()
    expect(tabs()[2].hasAttribute("data-active")).toBe(false)
  })

  it("groups the More sheet and marks the current page", async () => {
    const user = setup()
    await at("/usage")
    expect(tabs()[3].hasAttribute("data-active")).toBe(true)
    await user.click(tabs()[3])
    for (const g of ["Work", "Agent", "Host"]) expect(within(sheet()).getByText(g)).toBeTruthy()
    expect(items().map((x) => x.textContent?.trim())).toEqual([
      "Commands and skills",
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
    const commands = within(sheet()).getByRole("button", { name: "Commands and skills" })
    expect(svgClass(commands)).toBe(iconClass(ICONS.search))
    expect(sheet().textContent).not.toContain("⌘")
    const usage = within(sheet()).getByRole("link", { name: "Usage" })
    expect(usage.getAttribute("aria-current")).toBe("page")
    expect(usage.hasAttribute("data-active")).toBe(true)
  })

  it("marks System operations, not System, on the ops page", async () => {
    const user = setup()
    await at("/system/ops")
    await user.click(tabs()[3])
    const on = within(sheet())
      .getAllByRole("link")
      .filter((x) => x.getAttribute("aria-current") === "page")
      .map((x) => x.textContent?.trim())
    expect(on).toEqual(["System operations"])
  })

  it("sets the theme from the More sheet", async () => {
    const user = setup()
    prefs.theme = "system"
    await at("/chat")
    await user.click(tabs()[3])
    await user.click(within(sheet()).getByRole("button", { name: "Theme: System ▸" }))
    const choices = () =>
      within(sheet())
        .getAllByRole("button")
        .filter((b) => b.hasAttribute("aria-pressed"))
    expect(choices().map((x) => x.querySelector("span")?.textContent)).toEqual([
      "System",
      "Dark",
      "Light",
    ])
    expect(choices().map(svgClass)).toEqual([
      iconClass(ICONS.monitor),
      iconClass(ICONS.moon),
      iconClass(ICONS.sun),
    ])
    expect(choices()[0].lastElementChild?.textContent).toBe("✓")
    expect(choices()[0].getAttribute("aria-pressed")).toBe("true")
    expect(choices()[1].getAttribute("aria-pressed")).toBe("false")
    expect(sheet().textContent).not.toMatch(/🌙|☀️/)
    await user.click(choices()[2])
    expect(prefs.theme).toBe("light")
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "More" })).toBeNull())
  })

  it("closes the More sheet on Esc", async () => {
    const user = setup()
    await at("/chat")
    await user.click(tabs()[3])
    expect(sheet()).toBeTruthy()
    await user.keyboard("{Escape}")
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "More" })).toBeNull())
  })

  it("is a bottom sheet with the safe-area padding, a height limit, and a scroll body", async () => {
    const user = setup()
    await at("/chat")
    await user.click(tabs()[3])
    expect(sheet().className).toContain("max-h-[85dvh]")
    expect(sheet().className).toContain("pb-[env(safe-area-inset-bottom)]")
    expect(sheet().querySelector('[data-slot="more-body"]')!.className).toContain("overflow-y-auto")
  })

  it("stays open on a tap on a toast", async () => {
    const user = setup()
    await at("/chat")
    await user.click(tabs()[3])
    const toaster = document.createElement("div")
    toaster.setAttribute("data-sonner-toaster", "")
    toaster.innerHTML = "<button>Undo</button>"
    document.body.append(toaster)
    await user.click(within(toaster).getByRole("button", { name: "Undo" }))
    expect(screen.getByRole("dialog", { name: "More" })).toBeTruthy()
    toaster.remove()
  })

  it("hides the badge when nothing waits", async () => {
    cards.open = []
    await at("/chat")
    expect(tabs()[2].querySelector('[data-slot="badge"]')).toBeNull()
  })
})
