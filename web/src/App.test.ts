import { mount } from "@vue/test-utils"
import { defineComponent, h } from "vue"
import { afterEach, describe, expect, it, vi } from "vitest"
import App from "./App.vue"
import { cards } from "./stores/cards"

const Palette = defineComponent({
  name: "CommandPalette",
  props: { open: Boolean },
  emits: ["close"],
  setup: (p) => () => h("div", { class: "palette-stub", "data-open": String(p.open) }),
})

function mountApp(routeName: string, meta: { ownScroll?: boolean } = {}) {
  return mount(App, {
    attachTo: document.body,
    global: {
      mocks: { $route: { name: routeName, meta } },
      stubs: {
        StatusBar: { template: '<header class="statusbar-stub" />' },
        NavRail: { template: '<nav class="navrail-stub" />' },
        TabBar: { template: '<nav class="tabbar-stub" />' },
        RouterView: { template: '<div class="routerview-stub" />' },
        RestartBanner: { template: '<div class="restart-stub" />' },
        WaitingTray: { template: '<div class="tray-stub" />' },
        ConfirmDialog: true,
        ToastHost: true,
        CommandPalette: Palette,
      },
    },
  })
}

function key(init: KeyboardEventInit, target: EventTarget = document.body) {
  target.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, cancelable: true, ...init }))
}

afterEach(() => {
  document.body.innerHTML = ""
})

describe("App", () => {
  it("puts the restart banner under the status bar and hides it on sign-in", () => {
    let w = mountApp("jobs")
    expect(w.find(".statusbar-stub + .restart-stub").exists()).toBe(true)
    w.unmount()
    w = mountApp("signin")
    expect(w.find(".restart-stub").exists()).toBe(false)
  })

  it("has one aside slot in the shell, also on the sign-in page", () => {
    expect(mountApp("jobs").findAll(".shell > .aside-slot")).toHaveLength(1)
    expect(mountApp("signin").findAll(".shell > .aside-slot")).toHaveLength(1)
  })

  it("renders the one tray in the shell", () => {
    expect(mountApp("jobs").findAll(".tray-stub")).toHaveLength(1)
    expect(mountApp("signin").find(".tray-stub").exists()).toBe(false)
  })

  it("puts a page in the scroll box", () => {
    const w = mountApp("jobs")
    expect(w.find(".statusbar-stub").exists()).toBe(true)
    expect(w.find(".rail-slot .navrail-stub").exists()).toBe(true)
    expect(w.find(".tabbar-stub").exists()).toBe(true)
    expect(w.find("main.view > .scroll > .routerview-stub").exists()).toBe(true)
  })

  it("gives the chat no scroll box", () => {
    const w = mountApp("chat", { ownScroll: true })
    expect(w.find("main.view > .routerview-stub").exists()).toBe(true)
    expect(w.find(".scroll").exists()).toBe(false)
  })

  it("hides the shell on the sign-in page", () => {
    const w = mountApp("signin")
    expect(w.find(".statusbar-stub").exists()).toBe(false)
    expect(w.find(".navrail-stub").exists()).toBe(false)
    expect(w.find(".tabbar-stub").exists()).toBe(false)
    expect(w.find(".routerview-stub").exists()).toBe(true)
    expect(w.find("main.view > .scroll > .routerview-stub").exists()).toBe(true)
  })

  it("opens the palette on Ctrl+K and closes it on close", async () => {
    const w = mountApp("chat")
    expect(w.find(".palette-stub").attributes("data-open")).toBe("false")
    key({ key: "k", ctrlKey: true })
    await w.vm.$nextTick()
    expect(w.find(".palette-stub").attributes("data-open")).toBe("true")
    w.findComponent(Palette).vm.$emit("close")
    await w.vm.$nextTick()
    expect(w.find(".palette-stub").attributes("data-open")).toBe("false")
  })

  it("opens the palette on / only outside a text field", async () => {
    const w = mountApp("chat")
    const area = document.createElement("textarea")
    document.body.appendChild(area)
    key({ key: "/" }, area)
    await w.vm.$nextTick()
    expect(w.find(".palette-stub").attributes("data-open")).toBe("false")
    key({ key: "/" })
    await w.vm.$nextTick()
    expect(w.find(".palette-stub").attributes("data-open")).toBe("true")
  })

  it("announces each new card once in one polite live region", async () => {
    vi.useFakeTimers()
    cards.open = []
    const w = mountApp("jobs")
    const region = w.find(".announce")
    expect(region.attributes("aria-live")).toBe("polite")
    cards.open = [
      { id: "a1", kind: "approval", command: "rm -rf build/", summary: "" },
      { id: "q1", kind: "clarify", command: "", summary: "Which branch?" },
      { id: "k1", kind: "secret", command: "", summary: "", secret_name: "PYPI_TOKEN" },
    ] as never
    await w.vm.$nextTick()
    vi.advanceTimersByTime(50)
    expect(region.text()).toBe(
      "Approval needed: rm -rf build/. Question: Which branch?. Secret needed: PYPI_TOKEN",
    )
    cards.open = [...cards.open] as never
    await w.vm.$nextTick()
    vi.advanceTimersByTime(50)
    expect(region.text()).toBe(
      "Approval needed: rm -rf build/. Question: Which branch?. Secret needed: PYPI_TOKEN",
    )
    vi.useRealTimers()
  })
})
