import { flushPromises } from "@vue/test-utils"
import { render, screen, waitFor, within } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"
import type { Component } from "vue"
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
import { ICONS } from "../lib/icons"
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

async function open(props: { open: boolean } = { open: true }) {
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
  const r = render(CommandPalette, { props, global: { plugins: [router] } })
  await flushPromises()
  return { ...r, router }
}

const setup = () => userEvent.setup({ pointerEventsCheck: 0 })
const input = () => screen.getByRole("combobox") as HTMLInputElement
const options = () => screen.queryAllByRole("option")
const label = (o: HTMLElement) => o.querySelector("[data-label]")?.textContent ?? ""
const hint = (text: string) =>
  screen.getByText((_, el) => el?.tagName === "SPAN" && el.textContent === text)
// Returns the class of the SVG that an icon component draws.
const iconClass = (c: Component) =>
  render(c).container.querySelector("svg")?.getAttribute("class") ?? ""

beforeEach(() => {
  vi.mocked(api).mockClear()
  ui.trayOpen = false
})

describe("CommandPalette", () => {
  it("caps each group at 12 with a Show all row, and loads the bootstrap on each open", async () => {
    const user = setup()
    const { rerender } = await open()
    expect(api).toHaveBeenCalledWith("GET", "/api/bootstrap")
    expect(screen.getAllByRole("group")).toHaveLength(3)
    expect(screen.getByRole("group", { name: "Pages · 12 of 14" })).toBeTruthy()
    expect(screen.getByRole("group", { name: "Commands" })).toBeTruthy()
    expect(screen.getByRole("group", { name: "Skills" })).toBeTruthy()
    expect(options()).toHaveLength(16)
    expect(screen.getByRole("option", { name: "Show all 14" })).toBeTruthy()
    expect(screen.getByText("17 results")).toBeTruthy()
    await waitFor(() => expect(document.activeElement).toBe(input()))
    await user.click(screen.getByRole("option", { name: "Show all 14" }))
    expect(screen.getByRole("group", { name: "Pages" })).toBeTruthy()
    expect(options()).toHaveLength(17)
    await rerender({ open: false })
    await rerender({ open: true })
    await flushPromises()
    expect(api).toHaveBeenCalledTimes(2)
  })

  it("has the Waiting and the System operations rows", async () => {
    const user = setup()
    const { router, unmount } = await open()
    await user.type(input(), "backup")
    expect(options().map(label)).toEqual(["System operations"])
    await user.keyboard("{Enter}")
    await flushPromises()
    expect(router.currentRoute.value.name).toBe("ops")
    unmount()
    const again = await open()
    await user.type(input(), "waiting")
    await user.keyboard("{Enter}")
    expect(ui.trayOpen).toBe(true)
    expect(again.emitted().close).toHaveLength(1)
  })

  it("tells insert for a command and open for a page", async () => {
    const user = setup()
    await open()
    await waitFor(() => expect(hint("↵ to open")).toBeTruthy())
    await user.type(input(), "/")
    await waitFor(() => expect(hint("↵ to insert")).toBeTruthy())
  })

  it("uses the combobox pattern", async () => {
    const user = setup()
    await open()
    expect(input().getAttribute("aria-expanded")).toBe("true")
    expect(input().getAttribute("aria-controls")).toBe(screen.getByRole("listbox").id)
    await waitFor(() => expect(input().getAttribute("aria-activedescendant")).toBe(options()[0].id))
    input().focus()
    await user.keyboard("{ArrowDown}")
    expect(input().getAttribute("aria-activedescendant")).toBe(options()[1].id)
  })

  it("completes the highlighted command on Tab and keeps the typed rest", async () => {
    const user = setup()
    const { router } = await open()
    await user.type(input(), "/ne")
    await user.keyboard("{Tab}")
    expect(input().value).toBe("/new ")
    await user.clear(input())
    await user.type(input(), "/new today")
    await user.keyboard("{Enter}")
    await flushPromises()
    expect(router.currentRoute.value.query.draft).toBe("/new today")
  })

  it("has a Close button for a phone", async () => {
    const user = setup()
    const { emitted } = await open()
    const close = screen
      .getAllByRole("button", { name: "Close" })
      .find((b) => b.textContent?.trim() === "Close")!
    expect(close.className).toContain("tab:hidden")
    expect(close.className).toContain("min-h-11")
    await user.click(close)
    expect(emitted().close).toHaveLength(1)
  })

  it("filters and opens a page", async () => {
    const user = setup()
    const { router, emitted } = await open()
    await user.type(input(), "models")
    expect(options().map(label)).toEqual(["Models"])
    await waitFor(() => expect(options()[0].hasAttribute("data-highlighted")).toBe(true))
    await user.keyboard("{Enter}")
    await flushPromises()
    expect(router.currentRoute.value.name).toBe("models")
    expect(emitted().close).toHaveLength(1)
  })

  it("moves the highlight and puts a command in the composer", async () => {
    const user = setup()
    const { router } = await open()
    await user.type(input(), "/")
    expect(options().map(label)).toEqual(["/usage", "/new", "/release-notes"])
    await waitFor(() => expect(options()[0].hasAttribute("data-highlighted")).toBe(true))
    await user.keyboard("{ArrowDown}")
    expect(options()[1].hasAttribute("data-highlighted")).toBe(true)
    await user.keyboard("{Enter}")
    await flushPromises()
    expect(router.currentRoute.value.name).toBe("chat")
    expect(router.currentRoute.value.query.draft).toBe("/new ")
  })

  it("wraps the highlight at the ends", async () => {
    const user = setup()
    await open()
    await user.type(input(), "/")
    await waitFor(() => expect(options()[0].hasAttribute("data-highlighted")).toBe(true), {
      timeout: 3000,
    })
    await user.keyboard("{ArrowUp}")
    await waitFor(() => expect(options()[2].hasAttribute("data-highlighted")).toBe(true), {
      timeout: 3000,
    })
    await user.keyboard("{ArrowDown}")
    await waitFor(() => expect(options()[0].hasAttribute("data-highlighted")).toBe(true), {
      timeout: 3000,
    })
  })

  it("shows the empty state", async () => {
    const user = setup()
    await open()
    await user.type(input(), "zzz")
    expect(screen.getByText("No match for “zzz”")).toBeTruthy()
    expect(screen.getByText("Try a page name, or a command such as /usage.")).toBeTruthy()
  })

  it("draws the page icons from ICONS", async () => {
    await open()
    const chat = options().find((o) => label(o) === "Chat")!
    expect(chat.querySelector("svg")?.getAttribute("class")).toBe(iconClass(ICONS.chat))
    expect(input().parentElement?.querySelector("svg")?.getAttribute("class")).toBe(
      iconClass(ICONS.search),
    )
  })

  it("closes on Esc and on the scrim", async () => {
    const user = setup()
    const { emitted } = await open()
    await user.keyboard("{Escape}")
    await user.click(document.querySelector<HTMLElement>('[data-slot="dialog-overlay"]')!)
    expect(emitted().close).toHaveLength(2)
  })

  it("does not close on a tap on a toast", async () => {
    const user = setup()
    const { emitted } = await open()
    const toaster = document.createElement("div")
    toaster.setAttribute("data-sonner-toaster", "")
    toaster.innerHTML = "<button>Undo</button>"
    document.body.append(toaster)
    await user.click(within(toaster).getByRole("button", { name: "Undo" }))
    expect(emitted().close).toBeUndefined()
    toaster.remove()
  })

  it("gives the focus back to the element that had it at open", async () => {
    const user = setup()
    const opener = document.createElement("button")
    opener.textContent = "Commands"
    document.body.append(opener)
    opener.focus()
    const { rerender } = await open({ open: false })
    await rerender({ open: true })
    await flushPromises()
    await waitFor(() => expect(document.activeElement).toBe(input()))
    await user.keyboard("{Escape}")
    await rerender({ open: false })
    await waitFor(() => expect(document.activeElement).toBe(opener))
    opener.remove()
  })
})
