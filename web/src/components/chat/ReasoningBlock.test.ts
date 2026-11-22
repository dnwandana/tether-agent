import { mount } from "@vue/test-utils"
import { describe, expect, it } from "vitest"
import ReasoningBlock from "./ReasoningBlock.vue"

describe("ReasoningBlock", () => {
  it("is a closed block with the token count", () => {
    const w = mount(ReasoningBlock, {
      props: { text: "Runs() has no offset. <b>x</b>", tokens: 1812 },
    })
    const d = w.find("details.reasoning")
    expect(d.attributes("open")).toBeUndefined()
    expect(d.find("summary").text()).toBe("▸ Reasoning · 1,812 tokens")
    expect(d.find(".rs-b").text()).toBe("Runs() has no offset. <b>x</b>")
  })

  it("does not show with no text", () => {
    expect(
      mount(ReasoningBlock, { props: { text: "", tokens: 0 } })
        .find("details")
        .exists(),
    ).toBe(false)
  })
})
