import { render, screen } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
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
const box = () => document.querySelector<HTMLElement>('[data-slot="alert"]')
const noteBody = () => box()!.querySelector<HTMLElement>(".typeset.typeset-chat")!

describe("ResultNote", () => {
  it("renders nothing with no note", () => {
    render(ResultNote, { props: { note: null } })
    expect(box()).toBeNull()
  })

  it("shows the title, the time, and the Markdown body", () => {
    render(ResultNote, { props: { note: note() } })
    expect(box()!.querySelector("[data-title]")?.textContent).toBe("Doctor")
    expect(box()!.querySelector("[data-time]")?.textContent).toBe("09:05")
    const body = box()!.querySelector<HTMLElement>(".typeset.typeset-chat")!
    expect(body.querySelector("strong")?.textContent).toBe("12")
    expect(body.querySelector("li")?.textContent).toBe("chrome: OK")
    expect(screen.getByRole("status")).toBe(box())
    expect(box()!.dataset.kind).toBe("info")
  })

  it("shows the title as typed", () => {
    render(ResultNote, { props: { note: note({ title: "**Backup** <b>x</b>" }) } })
    const title = box()!.querySelector<HTMLElement>("[data-title]")!
    expect(title.textContent).toBe("**Backup** <b>x</b>")
    expect(title.querySelector("b")).toBeNull()
  })

  it("uses an alert for an error and a data-kind for each kind", () => {
    const r = render(ResultNote, {
      props: { note: note({ kind: "error", text: "⚠️ **/doctor failed**" }) },
    })
    expect(screen.getByRole("alert")).toBe(box())
    expect(box()!.dataset.kind).toBe("error")
    r.unmount()
    render(ResultNote, { props: { note: note({ kind: "warn" }) } })
    expect(box()!.dataset.kind).toBe("warn")
  })

  it("emits close on the X button", async () => {
    const user = userEvent.setup()
    const { emitted } = render(ResultNote, { props: { note: note() } })
    const close = screen.getByRole("button", { name: "Close" })
    expect(close.textContent?.trim()).toBe("")
    expect(close.querySelector("svg.lucide-x")).not.toBeNull()
    await user.click(close)
    expect(emitted().close).toHaveLength(1)
  })

  it("shows CircleCheck in the success color for an ok note", () => {
    render(ResultNote, { props: { note: note({ kind: "ok", text: "✅ Saved." }) } })
    expect(box()!.dataset.kind).toBe("ok")
    expect(screen.getByRole("status")).toBe(box())
    const icon = box()!.querySelector(":scope > svg")!
    expect(icon.classList).toContain("lucide-circle-check")
    expect(icon.classList).toContain("text-success")
  })

  it("drops a lead mark of the same tone from the body and keeps another", () => {
    const r = render(ResultNote, { props: { note: note({ kind: "ok", text: "✅ Saved." }) } })
    expect(noteBody().querySelectorAll("svg.glyph")).toHaveLength(0)
    expect(noteBody().textContent?.trim()).toBe("Saved.")
    r.unmount()
    render(ResultNote, { props: { note: note({ kind: "info", text: "⏸ Paused job x" }) } })
    expect(noteBody().querySelectorAll("svg.glyph")).toHaveLength(1)
    expect(noteBody().textContent).toContain("Paused job x")
  })
})
