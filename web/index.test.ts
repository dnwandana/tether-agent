// @vitest-environment node
import { readFileSync } from "node:fs"
import { describe, expect, it } from "vitest"

const html = readFileSync(new URL("./index.html", import.meta.url), "utf8")

describe("index.html", () => {
  it("loads one module script and mounts at #app", () => {
    expect(html).toContain('<div id="app"></div>')
    expect(html).toContain('<script type="module" src="/src/main.ts"></script>')
    expect(html).toContain("<title>Tether Agent Dashboard</title>")
    expect(html).toContain("viewport-fit=cover")
    expect(html).toContain('<html lang="en" class="tt">')
  })

  it("has no inline script and no font host", () => {
    expect(html).not.toMatch(/<script(?![^>]*\bsrc=)[^>]*>/)
    expect(html).not.toContain("fonts.googleapis")
    expect(html).not.toContain("@font-face")
  })

  it("links the three icons and has no empty icon", () => {
    expect(html).toContain('<link rel="icon" href="/favicon.svg" type="image/svg+xml" />')
    expect(html).toContain('<link rel="icon" href="/favicon.ico" sizes="32x32" />')
    expect(html).toContain('<link rel="apple-touch-icon" href="/apple-touch-icon.png" />')
    expect(html).not.toContain("data:,")
    expect(html).not.toContain('sizes="any"')
    expect(html).not.toContain("manifest")
  })

  it("has one theme-color meta with no content and no media", () => {
    expect(html.match(/<meta name="theme-color"[^>]*>/g)).toEqual(['<meta name="theme-color" />'])
  })

  it("ships the 180 px Apple touch icon", () => {
    const png = readFileSync(new URL("./public/apple-touch-icon.png", import.meta.url))
    expect([...png.subarray(0, 8)]).toEqual([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a])
    expect(png.readUInt32BE(16)).toBe(180)
    expect(png.readUInt32BE(20)).toBe(180)
  })
})
