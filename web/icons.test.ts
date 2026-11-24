// @vitest-environment node
import { readFileSync } from "node:fs"
import { describe, expect, it } from "vitest"

const pub = (f: string) => readFileSync(new URL("./public/" + f, import.meta.url))

describe("web/public icons", () => {
  it("ships the SVG favicon with the T-hook mark", () => {
    const svg = pub("favicon.svg").toString("utf8")
    expect(svg).toContain('viewBox="0 0 100 100"')
    expect(svg).toContain("M26 30 H74 M50 30 V58 C50 74 66 77 72 66")
    expect(svg).toContain('<circle cx="72" cy="66" r="6.5"')
  })

  it("ships an ICO file with the 16, 32, and 48 px images", () => {
    const ico = pub("favicon.ico")
    expect([...ico.subarray(0, 4)]).toEqual([0, 0, 1, 0])
    expect(ico.readUInt16LE(4)).toBe(3)
    // Each directory entry has 16 bytes. Its first byte is the width.
    expect([0, 1, 2].map((i) => ico[6 + i * 16])).toEqual([16, 32, 48])
  })
})
