import { mount } from "@vue/test-utils"
import { afterEach, describe, expect, it } from "vitest"
import { defineComponent, h, nextTick, ref, type Ref } from "vue"
import { useOverlay } from "./overlay"

// Box is an overlay with two buttons. It renders only while open is true.
const Box = defineComponent({
  props: {
    open: { type: Object as () => Ref<boolean>, required: true },
    modal: Boolean,
    name: String,
  },
  setup(props) {
    const el = ref<HTMLElement | null>(null)
    useOverlay(props.open, el, { onClose: () => (props.open.value = false), modal: props.modal })
    return () =>
      props.open.value
        ? h("div", { ref: el, class: "box", "data-name": props.name }, [
            h("button", { class: "first" }, "First"),
            h("button", { class: "last" }, "Last"),
          ])
        : null
  },
})

const key = (k: string, shift = false) => {
  const e = new KeyboardEvent("keydown", {
    key: k,
    shiftKey: shift,
    bubbles: true,
    cancelable: true,
  })
  ;(document.activeElement ?? document).dispatchEvent(e)
  return e
}

afterEach(() => {
  document.body.innerHTML = ""
})

describe("useOverlay", () => {
  it("closes only the top overlay on Esc and marks the event", async () => {
    const a = ref(false)
    const b = ref(false)
    mount(Box, { props: { open: a, name: "a" }, attachTo: document.body })
    mount(Box, { props: { open: b, name: "b" }, attachTo: document.body })
    a.value = true
    await nextTick()
    b.value = true
    await nextTick()
    const e = key("Escape")
    expect(e.defaultPrevented).toBe(true)
    expect([a.value, b.value]).toEqual([true, false])
    key("Escape")
    expect(a.value).toBe(false)
    const none = key("Escape")
    expect(none.defaultPrevented).toBe(false)
  })

  it("moves the focus in, traps Tab, and gives the focus back on close", async () => {
    const opener = document.createElement("button")
    document.body.appendChild(opener)
    opener.focus()
    const open = ref(false)
    const w = mount(Box, { props: { open, modal: true }, attachTo: document.body })
    open.value = true
    await nextTick()
    await nextTick()
    expect(document.activeElement?.className).toBe("first")
    ;(w.find(".last").element as HTMLElement).focus()
    const tab = key("Tab")
    expect(tab.defaultPrevented).toBe(true)
    expect(document.activeElement?.className).toBe("first")
    const back = key("Tab", true)
    expect(back.defaultPrevented).toBe(true)
    expect(document.activeElement?.className).toBe("last")
    key("Escape")
    await nextTick()
    await nextTick()
    expect(open.value).toBe(false)
    expect(document.activeElement).toBe(opener)
  })

  it("keeps the focus when the overlay is not modal", async () => {
    const opener = document.createElement("button")
    document.body.appendChild(opener)
    opener.focus()
    const open = ref(false)
    mount(Box, { props: { open }, attachTo: document.body })
    open.value = true
    await nextTick()
    await nextTick()
    expect(document.activeElement).toBe(opener)
    expect(key("Tab").defaultPrevented).toBe(false)
  })

  it("leaves the stack when the component unmounts while open", async () => {
    const a = ref(false)
    const b = ref(false)
    mount(Box, { props: { open: a }, attachTo: document.body })
    const wb = mount(Box, { props: { open: b }, attachTo: document.body })
    a.value = true
    await nextTick()
    b.value = true
    await nextTick()
    wb.unmount()
    key("Escape")
    expect(a.value).toBe(false)
  })
})
