import { readFileSync } from "node:fs"
import { describe, expect, it } from "vitest"

const read = (f: string) => readFileSync(new URL("./" + f, import.meta.url), "utf8")
const tokens = read("tokens.css")
const base = read("base.css")
const comps = read("components.css")
const all = tokens + base + comps

// Prettier formats the CSS. The checks compare the text with the white space collapsed and in
// lower case, so a change of the layout does not fail them.
const flat = (css: string) => css.replace(/\s+/g, " ").toLowerCase()

function has(css: string, text: string): void {
  expect(flat(css)).toContain(flat(text))
}

function block(css: string, selector: string): string {
  const s = flat(css)
  const i = s.indexOf(selector + " {")
  expect(i, selector).toBeGreaterThanOrEqual(0)
  return s.slice(i, s.indexOf("}", i))
}

describe("styles", () => {
  it("uses system fonts only", () => {
    expect(all).not.toContain("@font-face")
    expect(all).not.toContain("IBM Plex")
    expect(all).not.toContain("fonts.googleapis")
    has(tokens, '--font-sans: system-ui, -apple-system, "Segoe UI", sans-serif;')
    has(tokens, '--font-mono: ui-monospace, "SFMono-Regular", Menlo, monospace;')
  })

  it("has no canvas classes of the mockup", () => {
    expect(all).not.toMatch(/\.spec[\s{.,-]/)
    expect(all).not.toMatch(/^\.new\s*\{/m)
    expect(all).not.toMatch(/^\.phone\s*\{/m)
  })

  it("defines the four tokens in both themes", () => {
    const dark = block(tokens, ".theme-dark")
    const light = block(tokens, ".theme-light")
    for (const t of ["--on-accent:", "--seg-on:", "--toast-ok:", "--nav-on:"]) {
      expect(dark, t).toContain(t)
      expect(light, t).toContain(t)
    }
    has(dark, "--nav-on: #332E28;")
    has(light, "--on-accent: #FFFFFF;")
    has(light, "--seg-on: #FFFFFF;")
    has(light, "--toast-ok: #7BE0A5;")
    has(light, "--nav-on: #E3E6EB;")
  })

  it("defines each token that a rule uses", () => {
    const defined = new Set([...tokens.matchAll(/(--[a-z0-9-]+):/g)].map((m) => m[1]))
    defined.add("--dock-h")
    const used = new Set([...(base + comps).matchAll(/var\((--[a-z0-9-]+)/g)].map((m) => m[1]))
    for (const v of used) expect(defined.has(v), v).toBe(true)
  })

  it("has the breakpoints and the reduced-motion rule", () => {
    expect(comps).toContain("@media (max-width: 1023px)")
    expect(comps).toContain("@media (max-width: 599px)")
    expect(base).toContain("@media (prefers-reduced-motion: reduce)")
  })

  it("keeps the alignment rules of the prototype", () => {
    has(comps, ".cellrow { display: inline-flex; align-items: center;")
    has(comps, ".ei { height: 1lh; vertical-align: top; }")
    has(comps, "top: 50%; transform: translateY(-50%);")
    has(comps, ".search > .input { padding-left: 34px; }")
  })

  it("uses classes for the overlays, not ids", () => {
    expect(comps).not.toMatch(/#(ov|pop|toasts|aside-scrim|view)\b/)
    has(comps, ".ov.scrim")
    has(comps, ".toasts {")
    has(comps, "table.rt-cards")
    has(comps, ".chat-col .transcript { padding-bottom: calc(var(--dock-h, 96px) + 24px); }")
  })
})
