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

// Returns the declarations of the first rule with the selector, after a start text, as a sorted list.
function decls(css: string, selector: string, after = ""): string[] {
  const s = flat(css)
  const from = after ? s.indexOf(flat(after)) : 0
  expect(from, after).toBeGreaterThanOrEqual(0)
  const i = s.indexOf(selector + " {", from)
  expect(i, selector).toBeGreaterThanOrEqual(0)
  const body = s.slice(s.indexOf("{", i) + 1, s.indexOf("}", i))
  return body
    .split(";")
    .map((d) => d.trim())
    .filter(Boolean)
    .sort()
}

type Rule = { media: string; selectors: string[]; body: string; at: number }

// Returns each style rule with its @media text and its place in the file. It skips comments
// and @keyframes. The CSS has no nested @media.
function rules(css: string): Rule[] {
  const src = css.replace(/\/\*[\s\S]*?\*\//g, (m) => " ".repeat(m.length))
  const out: Rule[] = []
  let media = ""
  let head = ""
  let i = 0
  while (i < src.length) {
    const ch = src[i]
    if (ch === "{") {
      const h = head.trim()
      head = ""
      if (h.startsWith("@media")) {
        media = h.replace(/\s+/g, " ")
        i++
        continue
      }
      if (h.startsWith("@")) {
        let d = 1
        i++
        while (i < src.length && d > 0) {
          if (src[i] === "{") d++
          else if (src[i] === "}") d--
          i++
        }
        continue
      }
      const end = src.indexOf("}", i)
      out.push({
        media,
        // A comma inside :is(...) does not split the list.
        selectors: h.split(/,(?![^(]*\))/).map((s) => s.trim().replace(/\s+/g, " ")),
        body: src.slice(i + 1, end).replace(/\s+/g, " "),
        at: i,
      })
      i = end + 1
      continue
    }
    if (ch === "}") {
      media = ""
      head = ""
    } else head += ch
    i++
  }
  return out
}

describe("styles", () => {
  it("gives the dark tokens to .tt with no theme class", () => {
    expect(flat(tokens)).toContain(".tt, .tt.theme-dark {")
  })

  it("gives the light tokens to the light class and to a light OS with no class", () => {
    const light = decls(tokens, ".tt.theme-light")
    const media = decls(
      tokens,
      ".tt:not(.theme-dark):not(.theme-light)",
      "@media (prefers-color-scheme: light)",
    )
    expect(light.length).toBeGreaterThan(40)
    expect(media).toEqual(light)
    expect(light).toContain("color-scheme: light")
  })

  it("has the page scroll box", () => {
    const box = block(comps, ".scroll")
    expect(box).toContain("overflow-y: auto")
    expect(box).toContain("min-height: 0")
    expect(box).toContain("flex: 1 1 auto")
    expect(block(comps, ".table-wrap")).not.toContain("max-height")
    expect(flat(comps)).not.toContain(".cnote .https-note")
  })

  it("makes the side panel a column of the shell at 1024 px and wider", () => {
    expect(block(comps, ".aside-slot")).toContain("display: contents")
    const s = flat(comps)
    const wide = s.indexOf("@media (min-width: 1024px) { .aside-slot > .aside {")
    expect(wide).toBeGreaterThanOrEqual(0)
    const col = s.slice(wide, s.indexOf("}", wide))
    expect(col).toContain("flex: 0 0 380px")
    expect(col).toContain("border-left: 1px solid var(--border)")
    expect(col).toContain("position: static")
    expect(flat(comps)).toContain(".discard {")
  })

  it("has a rule for each series class of the usage chart", () => {
    const page = read("../pages/UsagePage.vue")
    for (const c of ["s1", "s2", "s3", "s4", "s5", "s6", "sbars", "sbar", "axis-x", "spike", "lg"])
      expect(comps, c).toMatch(new RegExp(`\\.${c}\\s*[{,.:]`))
    expect(page).toContain("s6")
  })

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

  it("has the breakpoints and stops each animation and transition for reduced motion", () => {
    expect(comps).toContain("@media (max-width: 1023px)")
    expect(comps).toContain("@media (max-width: 599px)")
    const calm = rules(base).filter((r) => r.media === "@media (prefers-reduced-motion: reduce)")
    expect(calm.length).toBeGreaterThan(0)
    const stopped = new Set(
      calm.filter((r) => r.body.includes("!important")).flatMap((r) => r.selectors),
    )
    for (const r of rules(comps)) {
      if (!/(?:^|;)\s*(animation|transition):/.test(r.body)) continue
      if (/(animation|transition): none/.test(r.body)) continue
      for (const s of r.selectors) expect(stopped.has(s), s).toBe(true)
    }
    expect(stopped.has(".pulse")).toBe(true)
    expect(stopped.has(".aside")).toBe(true)
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

  it("styles the state heading with a class", () => {
    expect(flat(comps)).not.toContain(".state h4")
    const t = block(comps, ".state .state-t")
    expect(t).toContain("font-size: var(--fs-14)")
    expect(t).toContain("font-weight: 600")
    expect(t).toContain("color: var(--text)")
  })

  it("has the quiet button, the wrap of a command, and 44 px card buttons on a phone", () => {
    const q = block(comps, ".btn-quiet")
    expect(q).toContain("background: transparent")
    expect(q).toContain("border-color: var(--border)")
    expect(q).toContain("color: var(--text-2)")
    expect(block(comps, ".btn-quiet:hover")).toContain("background: var(--surface-3)")
    const cmd = block(comps, ".cmd")
    expect(cmd).toContain("overflow-wrap: anywhere")
    expect(cmd).not.toContain("word-break: break-all")
    const s = flat(comps)
    const phone = s.lastIndexOf("@media (max-width: 599px) { .icard-row .btn {")
    expect(phone).toBeGreaterThan(s.lastIndexOf(".icard-row .btn, .dialog-f .btn {"))
    expect(s.slice(phone, s.indexOf("}", phone))).toContain("height: var(--h-touch)")
  })
})

const TAP = [
  ".btn",
  ".btn-sm",
  "td.act .btn",
  ".pager .btn",
  ".card-h .btn",
  ".section-h .btn",
  ".card-f .btn",
  ".toast .btn",
  ".icard-row .btn",
  ".dialog-f .btn",
  ".seg button",
  ".tabs a",
  ".tabs button",
  ".menu-item",
  ".sb-item",
  ".check",
  ".input",
  "select",
]
const MARK = "/* ---------- Phone tap targets ---------- */"

const sel = (s: string) => rules(comps).find((r) => r.selectors.includes(s))

describe("phone and headings", () => {
  it("keeps the phone tap block last and defines .axis-x and .chain once", () => {
    const at = comps.indexOf(MARK)
    expect(comps.slice(at).match(/@media/g)).toHaveLength(1)
    for (const s of [".axis-x", ".chain"])
      expect(
        rules(comps).filter((r) => r.selectors.length === 1 && r.selectors[0] === s),
      ).toHaveLength(1)
    expect(comps).not.toContain(".chain-node")
    expect(comps).not.toContain(".chain-arrow")
  })

  it("keeps each control at 44 px on a phone", () => {
    const at = comps.indexOf(MARK)
    expect(at, MARK).toBeGreaterThanOrEqual(0)
    const list = rules(comps)
    const phone = list.filter((r) => r.at > at && r.media === "@media (max-width: 599px)")
    const covered = new Set(
      phone.filter((r) => /min-height: 44px/.test(r.body)).flatMap((r) => r.selectors),
    )
    for (const s of TAP) expect(covered.has(s), s).toBe(true)
    const icon = phone.find((r) => r.selectors.includes(".icon-btn"))
    expect(icon?.body).toContain("min-width: 44px")
    const fields = phone.find(
      (r) => r.selectors.includes(".input") && r.selectors.includes("select"),
    )
    expect(fields?.body).toContain("font-size: 16px")
    const end = Math.max(...phone.map((r) => r.at))
    for (const r of list) {
      if (r.at <= end || r.media.includes("min-width: 600px")) continue
      if (!r.selectors.some((s) => TAP.includes(s))) continue
      const m = /(?:^|;)\s*(?:min-height|max-height|height):\s*([^;]+)/.exec(r.body)
      if (!m) continue
      const v = m[1].trim()
      const small = /var\(--h-(sm|md|lg)\)/.test(v) || (/^\d+px$/.test(v) && parseInt(v) < 44)
      expect(small, r.selectors.join(", ") + " " + v).toBe(false)
    }
  })

  it("keeps the desktop height of the card and dialog buttons out of the phone", () => {
    const r = rules(comps).find(
      (x) => x.selectors.includes(".icard-row .btn") && x.selectors.includes(".dialog-f .btn"),
    )
    expect(r?.media).toBe("@media (min-width: 600px)")
  })

  it("sizes card and section headings by class at every level", () => {
    const card = sel(".card-h .h")
    expect(card?.selectors).toContain(".card-h :is(h1, h2, h3, h4, h5, h6)")
    expect(card?.body).toContain("font-size: var(--fs-13)")
    const section = sel(".section-h .h")
    expect(section?.selectors).toContain(".section-h :is(h1, h2, h3, h4, h5, h6)")
    expect(section?.body).toContain("font-size: var(--fs-16)")
    expect(sel(".card-h h3")).toBeUndefined()
    expect(sel(".section-h h2")).toBeUndefined()
    has(base, ".tt h1, .tt h2, .tt h3, .tt h4, .tt h5, .tt h6,")
  })

  it("limits the height of the log viewer", () => {
    has(comps, ".log-view { max-height: 60vh; overflow-y: auto;")
    has(comps, ".log-view.is-wrap { white-space: pre-wrap; overflow-wrap: anywhere; }")
  })
})
