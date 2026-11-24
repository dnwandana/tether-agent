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

  it("emits close on ✕", async () => {
    const user = userEvent.setup()
    const { emitted } = render(ResultNote, { props: { note: note() } })
    const close = screen.getByRole("button", { name: "Close" })
    expect(close.textContent?.trim()).toBe("✕")
    await user.click(close)
    expect(emitted().close).toHaveLength(1)
  })
})
