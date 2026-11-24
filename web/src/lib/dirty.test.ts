import { mount } from "@vue/test-utils"
import { defineComponent, h, ref } from "vue"
import { describe, expect, it } from "vitest"
import { anyDirty, useDirty } from "./dirty"

function form(dirty = ref(false)) {
  const w = mount(
    defineComponent({
      setup() {
        useDirty(dirty)
        return () => h("form")
      },
    }),
  )
  return { w, dirty }
}

describe("dirty registry", () => {
  it("is true while a mounted form is dirty", () => {
    expect(anyDirty()).toBe(false)
    const a = form()
    const b = form()
    expect(anyDirty()).toBe(false)
    b.dirty.value = true
    expect(anyDirty()).toBe(true)
    b.dirty.value = false
    expect(anyDirty()).toBe(false)
    a.w.unmount()
    b.w.unmount()
  })

  it("forgets a form when it unmounts", () => {
    const a = form(ref(true))
    expect(anyDirty()).toBe(true)
    a.w.unmount()
    expect(anyDirty()).toBe(false)
  })
})
