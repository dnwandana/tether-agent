import { flushPromises, mount } from "@vue/test-utils"
import { afterEach, describe, expect, it, vi } from "vitest"
import { createMemoryHistory, createRouter } from "vue-router"

vi.mock("../api/client", () => ({
  api: vi.fn(async () => ({
    commands: [
      { name: "/usage", description: "Tokens and cost" },
      { name: "/new", description: "Start a new session" },
    ],
    skills: [{ name: "/release-notes", description: "Write release notes" }],
  })),
}))

import CommandPalette from "./CommandPalette.vue"
import { api } from "../api/client"

const names = [
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
]

async function open() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: names.map((name) => ({ name, path: "/" + name, component: { render: () => null } })),
  })
  router.push("/sessions")
  await router.isReady()
  const w = mount(CommandPalette, {
    props: { open: true },
    attachTo: document.body,
    global: { plugins: [router] },
  })
  await flushPromises()
  return { w, router }
}

afterEach(() => {
  document.body.innerHTML = ""
})

describe("CommandPalette", () => {
  it("lists the pages, the commands, and the skills", async () => {
    const { w } = await open()
    expect(api).toHaveBeenCalledWith("GET", "/api/bootstrap")
    expect(w.findAll(".pal-group").map((g) => g.text())).toEqual(["Pages", "Commands", "Skills"])
    expect(w.findAll(".pal-row")).toHaveLength(15)
    expect(w.find(".pal-foot").text()).toContain("15 results")
    expect(document.activeElement).toBe(w.find("input").element)
  })

  it("filters and opens a page", async () => {
    const { w, router } = await open()
    await w.find("input").setValue("models")
    const rows = w.findAll(".pal-row")
    expect(rows.map((r) => r.find(".pl").text())).toEqual(["Models"])
    expect(rows[0].classes()).toContain("is-hi")
    await w.find("input").trigger("keydown", { key: "Enter" })
    await flushPromises()
    expect(router.currentRoute.value.name).toBe("models")
    expect(w.emitted("close")).toHaveLength(1)
  })

  it("moves the highlight and puts a command in the composer", async () => {
    const { w, router } = await open()
    await w.find("input").setValue("/")
    expect(w.findAll(".pal-row").map((r) => r.find(".pl").text())).toEqual([
      "/usage",
      "/new",
      "/release-notes",
    ])
    await w.find("input").trigger("keydown", { key: "ArrowDown" })
    expect(w.findAll(".pal-row")[1].classes()).toContain("is-hi")
    await w.find("input").trigger("keydown", { key: "Enter" })
    await flushPromises()
    expect(router.currentRoute.value.name).toBe("chat")
    expect(router.currentRoute.value.query.draft).toBe("/new ")
  })

  it("shows the empty state", async () => {
    const { w } = await open()
    await w.find("input").setValue("zzz")
    expect(w.find(".pal-empty").text()).toContain("No match for “zzz”")
  })

  it("closes on Esc and on the scrim", async () => {
    const { w } = await open()
    await w.find("input").trigger("keydown", { key: "Escape" })
    await w.find(".ov").trigger("click")
    expect(w.emitted("close")).toHaveLength(2)
  })
})
