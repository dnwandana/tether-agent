import { mount } from "@vue/test-utils"
import { defineComponent, h } from "vue"
import { afterEach, describe, expect, it } from "vitest"
import App from "./App.vue"

const Palette = defineComponent({
  name: "CommandPalette",
  props: { open: Boolean },
  emits: ["close"],
  setup: (p) => () => h("div", { class: "palette-stub", "data-open": String(p.open) }),
})

function mountApp(routeName: string) {
  return mount(App, {
    attachTo: document.body,
    global: {
      mocks: { $route: { name: routeName } },
      stubs: {
        StatusBar: { template: '<header class="statusbar-stub" />' },
        NavRail: { template: '<nav class="navrail-stub" />' },
        TabBar: { template: '<nav class="tabbar-stub" />' },
        RouterView: { template: '<div class="routerview-stub" />' },
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
  it("shows the shell on a page", () => {
    const w = mountApp("chat")
    expect(w.find(".statusbar-stub").exists()).toBe(true)
    expect(w.find(".rail-slot .navrail-stub").exists()).toBe(true)
    expect(w.find(".tabbar-stub").exists()).toBe(true)
    expect(w.find("main.view .routerview-stub").exists()).toBe(true)
  })

  it("hides the shell on the sign-in page", () => {
    const w = mountApp("signin")
    expect(w.find(".statusbar-stub").exists()).toBe(false)
    expect(w.find(".navrail-stub").exists()).toBe(false)
    expect(w.find(".tabbar-stub").exists()).toBe(false)
    expect(w.find(".routerview-stub").exists()).toBe(true)
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
})
