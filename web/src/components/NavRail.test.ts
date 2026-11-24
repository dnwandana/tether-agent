import { afterEach, describe, expect, it, vi } from "vitest"
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
import NavRail from "./NavRail.vue"
import { BRAND_PATH, ICONS } from "../lib/icons"
import { cards } from "../stores/cards"

const ORDER = [
  "Chat",
  "Sessions",
  "Jobs",
  "Goals",
  "Approvals and security",
  "Skills",
  "Memory",
  "Usage",
  "Models",
  "System",
  "Access",
  "Getting started",
]

async function at(path: string) {
  const router = makeRouter()
  router.push(path)
  await router.isReady()
  return render(NavRail, { global: { plugins: [router] } })
}
function tablet() {
  vi.stubGlobal("matchMedia", (q: string) => ({
    matches: q === "(max-width: 1023px)",
    media: q,
    addEventListener: () => {},
    removeEventListener: () => {},
  }))
}
const links = () => within(screen.getByRole("navigation", { name: "Pages" })).getAllByRole("link")
const iconClass = (c: Component) =>
  render(c).container.querySelector("svg")?.getAttribute("class") ?? ""

describe("NavRail", () => {
  afterEach(() => vi.unstubAllGlobals())

  it("draws the T-hook mark in the brand tile", async () => {
    await at("/chat")
    const brand = screen.getByText("Tether").parentElement!
    const tile = brand.querySelector<HTMLElement>('[aria-hidden="true"]')!
    expect(tile.querySelector("svg path")?.getAttribute("d")).toBe(BRAND_PATH)
    expect(tile.querySelector("svg circle")).not.toBeNull()
  })

  it("lists the pages in order", async () => {
    await at("/chat")
    expect(links().map((l) => l.querySelector(".truncate")?.textContent)).toEqual(ORDER)
  })

  it("draws the page icons from ICONS", async () => {
    await at("/chat")
    const keys = [
      "chat",
      "sessions",
      "jobs",
      "goals",
      "security",
      "skills",
      "memory",
      "usage",
      "models",
      "system",
      "access",
      "start",
    ] as const
    expect(links().map((l) => l.querySelector("svg")?.getAttribute("class"))).toEqual(
      keys.map((k) => iconClass(ICONS[k])),
    )
    const search = screen.getByRole("button", { name: /Search/ })
    expect(search.querySelector("svg")?.getAttribute("class")).toBe(iconClass(ICONS.search))
  })

  it("marks the parent of a child page", async () => {
    const first = await at("/jobs/j1")
    const on = links().filter((l) => l.getAttribute("aria-current") === "page")
    expect(on).toHaveLength(1)
    expect(on[0].textContent).toContain("Jobs")
    first.unmount()
    await at("/system/ops")
    const ops = links().filter((l) => l.getAttribute("aria-current") === "page")
    expect(ops.map((l) => l.textContent?.trim())).toEqual(["System"])
  })

  it("shows the waiting badge on Approvals and security", async () => {
    cards.open = [{ id: "a1" }, { id: "a2" }] as never
    await at("/chat")
    const item = links().find((l) => l.textContent?.includes("Approvals and security"))!
    expect(within(item).getByText("2").getAttribute("data-slot")).toBe("badge")
  })

  it("keeps each name as hidden text and a tooltip on a tablet", async () => {
    tablet()
    const user = userEvent.setup()
    await at("/chat")
    expect(links().map((l) => l.querySelector(".sr-only")?.textContent)).toEqual(ORDER)
    expect(links().every((l) => !l.hasAttribute("title"))).toBe(true)
    await user.hover(screen.getByRole("link", { name: "Sessions" }))
    expect(await screen.findByRole("tooltip", { name: "Sessions" })).toBeTruthy()
    await user.keyboard("{Escape}")
    await waitFor(() => expect(screen.queryByRole("tooltip")).toBeNull())
    const search = screen.getByRole("button", { name: /Search/ })
    expect(search.querySelector(".sr-only")?.textContent).toBe("Search")
    expect(search.hasAttribute("title")).toBe(false)
    await user.hover(search)
    expect(await screen.findByRole("tooltip", { name: "Search (Ctrl K)" })).toBeTruthy()
  })

  it("shows no tooltip on a wide screen", async () => {
    const user = userEvent.setup()
    await at("/chat")
    await user.hover(screen.getByRole("link", { name: "Sessions" }))
    await new Promise((r) => setTimeout(r, 20))
    expect(screen.queryByRole("tooltip")).toBeNull()
  })
})
