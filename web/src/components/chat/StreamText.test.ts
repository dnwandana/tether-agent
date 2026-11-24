import { describe, expect, it } from "vitest"
import { render, screen } from "@testing-library/vue"
import StreamText from "./StreamText.vue"

const body = (c: Element) => c.querySelector('[data-slot="message-content"]')!

describe("StreamText", () => {
  it("renders safe Markdown and the cursor while it runs", () => {
    const r = render(StreamText, { props: { text: "**done** <b>x</b>", running: true } })
    const md = body(r.container).querySelector(".typeset.typeset-chat")!
    expect(md.classList.contains("typeset-chat")).toBe(true)
    expect(md.innerHTML).toContain("<strong>done</strong>")
    expect(md.innerHTML).toContain("&lt;b&gt;x&lt;/b&gt;")
    const cursor = screen.getByText("▉")
    expect(cursor.getAttribute("aria-hidden")).toBe("true")
    expect(body(r.container).contains(cursor)).toBe(true)
  })

  it("keeps a fenced code block as plain pre text", () => {
    const r = render(StreamText, { props: { text: "```go\nfunc a() {}\n```", running: false } })
    const pre = body(r.container).querySelector("pre")!
    expect(pre.textContent).toContain("func a() {}")
    expect(pre.querySelector("span[style]")).toBeNull()
  })

  it("has no cursor when the turn ended", () => {
    render(StreamText, { props: { text: "Done.", running: false } })
    expect(screen.queryByText("▉")).toBeNull()
  })
})
