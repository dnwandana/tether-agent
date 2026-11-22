import { mount } from "@vue/test-utils"
import { describe, expect, it } from "vitest"
import StreamText from "./StreamText.vue"

describe("StreamText", () => {
  it("renders safe Markdown and the cursor while it runs", () => {
    const w = mount(StreamText, { props: { text: "**done** <b>x</b>", running: true } })
    expect(w.find(".answer").html()).toContain("<strong>done</strong>")
    expect(w.find(".answer").html()).toContain("&lt;b&gt;x&lt;/b&gt;")
    expect(w.find(".cursor").text()).toBe("▉")
  })

  it("has no cursor when the turn ended", () => {
    expect(
      mount(StreamText, { props: { text: "Done.", running: false } })
        .find(".cursor")
        .exists(),
    ).toBe(false)
  })
})
