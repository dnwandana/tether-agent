import { flushPromises, mount } from "@vue/test-utils"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
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
import { ui } from "../stores/ui"

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
  "ops",
  "access",
  "start",
]

async function open() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: names.map((name) => ({
      name,
      path: name === "ops" ? "/system/ops" : "/" + name,
      component: { render: () => null },
    })),
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

beforeEach(() => {
  vi.mocked(api).mockClear()
  ui.trayOpen = false
})

afterEach(() => {
  document.body.innerHTML = ""
})

describe("CommandPalette", () => {
  it("caps each group at 12 with a Show all row, and loads the bootstrap on each open", async () => {
    const { w } = await open()
    expect(api).toHaveBeenCalledWith("GET", "/api/bootstrap")
    expect(w.findAll(".pal-group").map((g) => g.text())).toEqual([
      "Pages · 12 of 14",
      "Commands",
      "Skills",
    ])
    expect(w.findAll(".pal-row")).toHaveLength(16)
    expect(w.find(".pal-row.pal-all").text()).toBe("Show all 14")
    expect(w.find(".pal-foot").text()).toContain("17 results")
    expect(document.activeElement).toBe(w.find("input").element)
    await w.find(".pal-row.pal-all").trigger("click")
    expect(w.findAll(".pal-group")[0].text()).toBe("Pages")
    expect(w.findAll(".pal-row")).toHaveLength(17)
    await w.setProps({ open: false })
    await w.setProps({ open: true })
    await flushPromises()
    expect(api).toHaveBeenCalledTimes(2)
  })

  it("has the Waiting and the System operations rows", async () => {
    const { w, router } = await open()
    await w.find("input").setValue("backup")
    expect(w.findAll(".pal-row").map((r) => r.find(".pl").text())).toEqual(["System operations"])
    await w.find("input").trigger("keydown", { key: "Enter" })
    await flushPromises()
    expect(router.currentRoute.value.name).toBe("ops")
    const again = await open()
    await again.w.find("input").setValue("waiting")
    await again.w.find("input").trigger("keydown", { key: "Enter" })
    expect(ui.trayOpen).toBe(true)
    expect(again.w.emitted("close")).toHaveLength(1)
  })

  it("tells insert for a command and open for a page", async () => {
    const { w } = await open()
    expect(w.find(".pal-foot").text()).toContain("↵ to open")
    await w.find("input").setValue("/")
    expect(w.find(".pal-foot").text()).toContain("↵ to insert")
  })

  it("uses the combobox pattern", async () => {
    const { w } = await open()
    const input = w.find("input")
    expect(input.attributes("role")).toBe("combobox")
    expect(input.attributes("aria-expanded")).toBe("true")
    expect(input.attributes("aria-controls")).toBe(w.find('[role="listbox"]').attributes("id"))
    expect(input.attributes("aria-activedescendant")).toBe(
      w.findAll(".pal-row")[0].attributes("id"),
    )
    await input.trigger("keydown", { key: "ArrowDown" })
    expect(input.attributes("aria-activedescendant")).toBe(
      w.findAll(".pal-row")[1].attributes("id"),
    )
  })

  it("completes the highlighted command on Tab and keeps the typed rest", async () => {
    const { w, router } = await open()
    await w.find("input").setValue("/ne")
    await w.find("input").trigger("keydown", { key: "Tab" })
    expect((w.find("input").element as HTMLInputElement).value).toBe("/new ")
    await w.find("input").setValue("/new today")
    await w.find("input").trigger("keydown", { key: "Enter" })
    await flushPromises()
    expect(router.currentRoute.value.query.draft).toBe("/new today")
  })

  it("has a Close button for a phone", async () => {
    const { w } = await open()
    const close = w.find(".pal-close")
    expect(close.text()).toBe("Close")
    expect(close.classes()).toContain("btn-touch")
    expect(close.classes()).toContain("only-phone")
    await close.trigger("click")
    expect(w.emitted("close")).toHaveLength(1)
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
    document.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }),
    )
    await w.find(".ov").trigger("click")
    expect(w.emitted("close")).toHaveLength(2)
  })
})
