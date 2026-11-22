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
import NavRail from "./NavRail.vue"
import { cards } from "../stores/cards"

async function at(path: string) {
  const router = makeRouter()
  router.push(path)
  await router.isReady()
  return mount(NavRail, { global: { plugins: [router] } })
}

describe("NavRail", () => {
  it("lists the pages in order", async () => {
    const w = await at("/chat")
    expect(w.findAll(".rail-item .truncate").map((x) => x.text())).toEqual([
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
    ])
  })

  it("marks the parent of a child page", async () => {
    const w = await at("/jobs/j1")
    const on = w.findAll(".rail-item.is-on")
    expect(on).toHaveLength(1)
    expect(on[0].text()).toContain("Jobs")
    expect(on[0].attributes("aria-current")).toBe("page")
    expect((await at("/system/ops")).find(".rail-item.is-on").text()).toContain("System")
  })

  it("shows the waiting badge on Approvals and security", async () => {
    cards.open = [{ id: "a1" }, { id: "a2" }] as never
    const w = await at("/chat")
    const item = w.findAll(".rail-item").find((x) => x.text().includes("Approvals and security"))!
    expect(item.find(".badge").text()).toBe("2")
  })
})
