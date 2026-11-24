import { mount } from "@vue/test-utils"
import { describe, expect, it } from "vitest"
import BrandMark from "./BrandMark.vue"

describe("BrandMark", () => {
  it("draws the T-hook path and the dot in currentColor on a hidden tile", () => {
    const w = mount(BrandMark)
    const tile = w.find('span[data-slot="brand-mark"]')
    expect(tile.exists()).toBe(true)
    expect(tile.attributes("aria-hidden")).toBe("true")
    const svg = tile.find("svg")
    expect(svg.attributes("viewBox")).toBe("0 0 24 24")
    expect(svg.attributes("width")).toBe("24")
    expect(svg.attributes("height")).toBe("24")
    const paths = svg.findAll("path")
    expect(paths).toHaveLength(1)
    expect(paths[0].attributes("d")).toBe("M6.24 7.2H17.76M12 7.2v6.72c0 3.84 3.84 4.56 5.28 1.92")
    expect(paths[0].attributes("fill")).toBe("none")
    expect(paths[0].attributes("stroke")).toBe("currentColor")
    expect(paths[0].attributes("stroke-width")).toBe("3.1")
    expect(paths[0].attributes("stroke-linecap")).toBe("round")
    const dot = svg.find("circle")
    expect([dot.attributes("cx"), dot.attributes("cy"), dot.attributes("r")]).toEqual([
      "17.28",
      "15.84",
      "1.56",
    ])
    expect(dot.attributes("fill")).toBe("currentColor")
    expect(dot.attributes("stroke")).toBe("none")
  })
})
