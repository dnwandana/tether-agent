import { mount } from "@vue/test-utils"
import { describe, expect, it } from "vitest"
import CostFooter from "./CostFooter.vue"

const cost = {
  model: "glm-4.6",
  provider: "zai",
  calls: 6,
  in: 61400,
  cached: 52000,
  out: 2300,
  usd: 0.0291,
  unpriced: false,
  seconds: 38,
  answered_by: "",
}

describe("CostFooter", () => {
  it("shows the turn cost", () => {
    const w = mount(CostFooter, { props: { cost } })
    expect(w.findAll(".cost-foot span").map((s) => s.text())).toEqual([
      "glm-4.6",
      "6 calls",
      "61.4k in (52.0k cached)",
      "2.3k out",
      "$0.0291",
      "38 s",
    ])
  })

  it("marks an unknown price and a fallback answer", () => {
    const w = mount(CostFooter, {
      props: {
        cost: {
          ...cost,
          unpriced: true,
          answered_by: "backup",
          in: 1_250_000,
          out: 512,
          seconds: 125,
        },
      },
    })
    const t = w.find(".cost-foot").text()
    expect(t).toContain("glm-4.6 · answered by backup")
    expect(t).toContain("1.3M in")
    expect(t).toContain("512 out")
    expect(t).toContain("$0.0291+")
    expect(t).toContain("2 m 5 s")
  })
})
