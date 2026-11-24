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
})
