import { render, screen } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import { defineComponent, h, nextTick } from "vue"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

vi.mock("./lib/update", () => ({ reloadNow: vi.fn(), setVersion: vi.fn(), onNewBuild: vi.fn() }))

import App from "./App.vue"
import { reloadNow } from "./lib/update"
import { cards } from "./stores/cards"
import { ui } from "./stores/ui"

const Palette = defineComponent({
  name: "CommandPalette",
  props: { open: Boolean },
  emits: ["close"],
  setup:
    (p, { emit }) =>
    () =>
      h("button", {
        "data-testid": "palette",
        "data-open": String(p.open),
        onClick: () => emit("close"),
      }),
})

function mountApp(routeName: string, meta: { ownScroll?: boolean } = {}) {
  return render(App, {
    global: {
      mocks: { $route: { name: routeName, meta } },
      stubs: {
        StatusBar: { template: '<header data-testid="statusbar" />' },
        NavRail: { template: '<nav data-testid="navrail" />' },
        TabBar: { template: '<nav data-testid="tabbar" />' },
        RouterView: { template: '<div data-testid="routerview" />' },
        RestartBanner: { template: '<div data-testid="restart" />' },
        WaitingTray: { template: '<div data-testid="tray" />' },
        ConfirmDialog: true,
        AppToaster: { template: '<div data-testid="toaster" />' },
        CommandPalette: Palette,
      },
    },
  })
}

function key(init: KeyboardEventInit, target: EventTarget = document.body) {
  target.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, cancelable: true, ...init }))
}
const shellOf = () => screen.getByRole("main").parentElement!

beforeEach(() => {
  ui.updateReady = false
})
afterEach(() => vi.mocked(reloadNow).mockClear())

describe("App", () => {
  it("puts the restart banner under the status bar and hides it on sign-in", () => {
    const r = mountApp("jobs")
    expect(screen.getByTestId("statusbar").nextElementSibling).toBe(screen.getByTestId("restart"))
    r.unmount()
    mountApp("signin")
    expect(screen.queryByTestId("restart")).toBeNull()
  })

  it("has one aside slot in the shell, also on the sign-in page", () => {
    const r = mountApp("jobs")
    expect(shellOf().querySelectorAll(":scope > .aside-slot")).toHaveLength(1)
    expect(shellOf().lastElementChild?.classList.contains("aside-slot")).toBe(true)
    r.unmount()
    mountApp("signin")
    expect(shellOf().querySelectorAll(":scope > .aside-slot")).toHaveLength(1)
  })

  it("renders the one tray in the shell", () => {
    const r = mountApp("jobs")
    expect(screen.getAllByTestId("tray")).toHaveLength(1)
    r.unmount()
    mountApp("signin")
    expect(screen.queryByTestId("tray")).toBeNull()
  })

  it("puts a page in the scroll box", () => {
    mountApp("jobs")
    expect(screen.getByTestId("statusbar")).toBeTruthy()
    const rail = screen.getByTestId("navrail").parentElement!
    expect(rail.parentElement).toBe(shellOf())
    expect(rail.className).toContain("max-tab:hidden")
    expect(rail.className).toContain("max-lg:w-16")
    expect(screen.getByTestId("tabbar")).toBeTruthy()
    const view = screen.getByTestId("routerview")
    expect(view.parentElement?.classList.contains("scroll")).toBe(true)
    expect(view.parentElement?.parentElement).toBe(screen.getByRole("main"))
  })

  it("gives the chat no scroll box", () => {
    mountApp("chat", { ownScroll: true })
    expect(screen.getByTestId("routerview").parentElement).toBe(screen.getByRole("main"))
    expect(document.querySelector(".scroll")).toBeNull()
  })

  it("hides the shell on the sign-in page", () => {
    mountApp("signin")
    expect(screen.queryByTestId("statusbar")).toBeNull()
    expect(screen.queryByTestId("navrail")).toBeNull()
    expect(screen.queryByTestId("tabbar")).toBeNull()
    const view = screen.getByTestId("routerview")
    expect(view.parentElement?.classList.contains("scroll")).toBe(true)
    expect(view.parentElement?.parentElement).toBe(screen.getByRole("main"))
  })

  it("opens the palette on Ctrl+K and closes it on close", async () => {
    const user = userEvent.setup()
    mountApp("chat")
    expect(screen.getByTestId("palette").getAttribute("data-open")).toBe("false")
    key({ key: "k", ctrlKey: true })
    await nextTick()
    expect(screen.getByTestId("palette").getAttribute("data-open")).toBe("true")
    await user.click(screen.getByTestId("palette"))
    expect(screen.getByTestId("palette").getAttribute("data-open")).toBe("false")
    key({ key: "k", metaKey: true })
    await nextTick()
    expect(screen.getByTestId("palette").getAttribute("data-open")).toBe("true")
  })

  it("opens the palette on / only outside a text field", async () => {
    mountApp("chat")
    const area = document.createElement("textarea")
    document.body.appendChild(area)
    key({ key: "/" }, area)
    await nextTick()
    expect(screen.getByTestId("palette").getAttribute("data-open")).toBe("false")
    key({ key: "/" })
    await nextTick()
    expect(screen.getByTestId("palette").getAttribute("data-open")).toBe("true")
    area.remove()
  })

  it("announces each new card once in one polite live region", async () => {
    vi.useFakeTimers()
    cards.open = []
    mountApp("jobs")
    const region = document.querySelector<HTMLElement>(".announce")!
    expect(region.getAttribute("aria-live")).toBe("polite")
    expect(region.classList.contains("sr-only")).toBe(true)
    cards.open = [
      { id: "a1", kind: "approval", command: "rm -rf build/", summary: "" },
      { id: "q1", kind: "clarify", command: "", summary: "Which folder?" },
      { id: "k1", kind: "secret", command: "", summary: "", secret_name: "PYPI_TOKEN" },
    ] as never
    await nextTick()
    vi.advanceTimersByTime(50)
    expect(region.textContent).toBe(
      "Approval needed: rm -rf build/. Question: Which folder?. Secret needed: PYPI_TOKEN",
    )
    cards.open = [...cards.open] as never
    await nextTick()
    vi.advanceTimersByTime(50)
    expect(region.textContent).toBe(
      "Approval needed: rm -rf build/. Question: Which folder?. Secret needed: PYPI_TOKEN",
    )
    vi.useRealTimers()
  })

  it("shows the update banner from the store flag and reloads on Reload", async () => {
    const user = userEvent.setup()
    mountApp("jobs")
    expect(screen.queryByText(/A new version is ready/)).toBeNull()
    ui.updateReady = true
    await nextTick()
    const banner = screen.getByText(/A new version is ready ·/).closest('[role="status"]')!
    expect(banner).not.toBeNull()
    await user.click(screen.getByRole("button", { name: "Reload" }))
    expect(reloadNow).toHaveBeenCalledTimes(1)
  })

  it("renders the toaster once, also on the sign-in page", () => {
    const r = mountApp("jobs")
    expect(screen.getAllByTestId("toaster")).toHaveLength(1)
    r.unmount()
    mountApp("signin")
    expect(screen.getAllByTestId("toaster")).toHaveLength(1)
  })
})
