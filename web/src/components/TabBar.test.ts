import { mount } from "@vue/test-utils"
import { describe, expect, it, vi } from "vitest"
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
import { cards } from "../stores/cards"

async function at(path: string) {
  const router = makeRouter()
  router.push(path)
  await router.isReady()
  return mount(TabBar, { global: { plugins: [router], stubs: { WaitingTray: true } } })
}

describe("TabBar", () => {
  it("has four tabs and the waiting badge", async () => {
    cards.open = [{ id: "a1" }, { id: "a2" }, { id: "a3" }] as never
    const w = await at("/chat")
    expect(w.findAll(".tab").map((t) => t.find("span").text())).toEqual([
      "Chat",
      "Jobs",
      "Approvals",
      "More",
    ])
    expect(w.findAll(".tab")[2].find(".badge").text()).toBe("3")
    expect(w.findAll(".tab")[0].classes()).toContain("is-on")
  })

  it("opens the tray from Approvals", async () => {
    const w = await at("/chat")
    await w.findAll(".tab")[2].trigger("click")
    expect(w.findComponent({ name: "WaitingTray" }).props("open")).toBe(true)
  })

  it("opens the More sheet with the other pages", async () => {
    const w = await at("/usage")
    expect(w.findAll(".tab")[3].classes()).toContain("is-on")
    await w.findAll(".tab")[3].trigger("click")
    const items = w.findAll(".ov .li").map((x) => x.text())
    expect(items).toEqual(
      expect.arrayContaining([
        "Sessions",
        "Goals",
        "Approvals and security",
        "Skills",
        "Memory",
        "Usage",
        "Models",
        "System",
        "Access",
        "Getting started",
        "Commands and skills",
      ]),
    )
  })

  it("hides the badge when nothing waits", async () => {
    cards.open = []
    const w = await at("/chat")
    expect(w.findAll(".tab")[2].find(".badge").exists()).toBe(false)
  })
})
