import { mount } from "@vue/test-utils"
import { describe, expect, it } from "vitest"
import ResultNote from "./ResultNote.vue"
import type { ResultNoteData } from "../lib/command"

const note = (over: Partial<ResultNoteData> = {}): ResultNoteData => ({
  title: "Doctor",
  at: new Date(2026, 10, 23, 9, 5),
  text: "**12** checks pass.\n- chrome: OK",
  kind: "info",
  ...over,
})

describe("ResultNote", () => {
  it("renders nothing with no note", () => {
    const w = mount(ResultNote, { props: { note: null } })
    expect(w.find(".rnote").exists()).toBe(false)
  })

  it("shows the title, the time, and the Markdown body", () => {
    const w = mount(ResultNote, { props: { note: note() } })
    expect(w.find(".rnote-t").text()).toBe("Doctor")
    expect(w.find(".rnote-at").text()).toBe("09:05")
    expect(w.find(".rnote-b strong").text()).toBe("12")
    expect(w.find(".rnote-b li").text()).toBe("chrome: OK")
    expect(w.find(".rnote").attributes("role")).toBe("status")
    expect(w.find(".rnote").classes()).toContain("rnote-info")
  })

  it("shows the title as typed", () => {
    const w = mount(ResultNote, { props: { note: note({ title: "**Backup** <b>x</b>" }) } })
    expect(w.find(".rnote-t").text()).toBe("**Backup** <b>x</b>")
    expect(w.find(".rnote-t b").exists()).toBe(false)
  })

  it("uses an alert for an error and a class for each kind", () => {
    const w = mount(ResultNote, {
      props: { note: note({ kind: "error", text: "⚠️ **/doctor failed**" }) },
    })
    expect(w.find(".rnote").attributes("role")).toBe("alert")
    expect(w.find(".rnote").classes()).toContain("rnote-error")
    const warn = mount(ResultNote, { props: { note: note({ kind: "warn" }) } })
    expect(warn.find(".rnote").classes()).toContain("rnote-warn")
  })

  it("emits close on ✕", async () => {
    const w = mount(ResultNote, { props: { note: note() } })
    await w.find('button[aria-label="Close"]').trigger("click")
    expect(w.emitted("close")).toHaveLength(1)
  })
})
