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

  it("makes the side panel a column at 1440 px and a drawer from 1024 px to 1439 px", () => {
    expect(block(comps, ".aside-slot")).toContain("display: contents")
    const s = flat(comps)
    expect(s).not.toContain("@media (min-width: 1024px) { .aside-slot > .aside {")
    const wide = s.indexOf("@media (min-width: 1440px) { .aside-slot > .aside {")
    expect(wide).toBeGreaterThanOrEqual(0)
    const col = s.slice(wide, s.indexOf("}", wide))
    expect(col).toContain("flex: 0 0 380px")
    expect(col).toContain("border-left: 1px solid var(--border)")
    expect(col).toContain("position: static")
    const mid = rules(comps).filter(
      (r) => r.media === "@media (min-width: 1024px) and (max-width: 1439px)",
    )
    const body = (sel: string) => mid.find((r) => r.selectors.includes(sel))?.body ?? ""
    expect(body(".aside-slot > .aside")).toContain("position: fixed")
    expect(body(".aside-slot > .aside")).toContain("transform: translateX(100%)")
    expect(body(".aside-slot > .aside.is-open")).toContain("transform: none")
    expect(body(".aside-slot > .aside-scrim.is-open")).toContain("display: block")
    expect(body(".aside-slot > .aside-scrim.is-open")).toContain("background: var(--scrim)")
    expect(body(".aside")).toContain("transition: transform 0.2s ease")
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
    has(light, "--nav-on: #EBE5DC;")
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

const STONE: [string, string][] = [
  ["--bg", "#f4f1ec"],
  ["--surface-1", "#fdfbf8"],
  ["--surface-2", "#fdfbf8"],
  ["--surface-3", "#ece7e0"],
  ["--surface-inset", "#f8f5f1"],
  ["--border", "#e2dcd3"],
  ["--border-strong", "#c9c0b3"],
  ["--nav-on", "#ebe5dc"],
  ["--text", "#1b1814"],
  ["--text-2", "#4a4339"],
  ["--text-3", "#6b6256"],
  ["--accent", "#17706a"],
  ["--accent-strong", "#17706a"],
  ["--new", "#17706a"],
  ["--focus", "#17706a"],
  ["--accent-hover", "#125c57"],
  ["--accent-tint", "#e1efec"],
  ["--scrim", "rgba(27, 24, 20, 0.45)"],
  ["--shadow-sm", "0 1px 2px rgba(27, 24, 20, 0.08)"],
  ["--shadow-md", "0 4px 12px rgba(27, 24, 20, 0.1), 0 1px 2px rgba(27, 24, 20, 0.06)"],
  ["--shadow-lg", "0 16px 40px rgba(27, 24, 20, 0.16), 0 2px 6px rgba(27, 24, 20, 0.08)"],
]
const KEPT: [string, string][] = [
  ["--on-accent", "#ffffff"],
  ["--seg-on", "#ffffff"],
  ["--text-inv", "#ffffff"],
  ["--toast-ok", "#7be0a5"],
  ["--success", "#17754b"],
  ["--warning", "#8f5300"],
  ["--danger", "#be2626"],
  ["--info", "#0a5faf"],
  ["--diff-add", "#e3f5ea"],
  ["--diff-del-text", "#9e1f1f"],
  ["--mark", "#fbe7a8"],
]

describe("Stone light", () => {
  it("gives both light blocks the Stone light values and keeps the other tokens", () => {
    const light = decls(tokens, ".tt.theme-light")
    const media = decls(
      tokens,
      ".tt:not(.theme-dark):not(.theme-light)",
      "@media (prefers-color-scheme: light)",
    )
    for (const [k, v] of [...STONE, ...KEPT]) {
      expect(light, k).toContain(`${k}: ${v}`)
      expect(media, k).toContain(`${k}: ${v}`)
    }
  })

  it("has no indigo accent and no graphite shadow", () => {
    expect(flat(tokens)).not.toContain("#4344c9")
    expect(flat(tokens)).not.toContain("rgba(18, 21, 26")
  })

  it("names the palette over the class block", () => {
    has(tokens, "/* Stone light: warm stone neutrals, petrol-teal accent */ .tt.theme-light {")
  })
})

// Returns the one rule of components.css with the selector s in the media block.
const one = (s: string, media = "") => {
  const r = rules(comps).find((x) => x.media === media && x.selectors.includes(s))
  expect(r, s).toBeDefined()
  return r!
}

describe("shared rules", () => {
  it("adds the shared page rules above the phone tap block", () => {
    const want: [string, string[]][] = [
      [".page-stack", ["display: flex", "flex-direction: column", "gap: var(--sp-6)"]],
      [
        ".cell-title",
        [
          "display: inline-block",
          "max-width: 40ch",
          "overflow: hidden",
          "text-overflow: ellipsis",
          "white-space: nowrap",
        ],
      ],
      [".card > .state", ["border: 0"]],
      [".md.clamp-3", ["white-space: normal"]],
      [".table .mono.wrap-any", ["white-space: normal"]],
      [".toolbar > .input", ["width: auto", "flex: 0 1 200px"]],
      [".toolbar > .input.grow-search", ["flex: 1 1 240px"]],
      [
        ".help-link",
        [
          "display: inline-flex",
          "min-width: 32px",
          "min-height: 32px",
          "margin-left: var(--sp-2)",
          "color: var(--text-3)",
          "text-decoration: none",
        ],
      ],
      [".help-link:hover", ["color: var(--text)"]],
      [".page-h .search", ["flex: 0 1 320px"]],
    ]
    for (const [s, ds] of want) {
      const r = one(s)
      expect(r.at, s).toBeLessThan(comps.indexOf(MARK))
      for (const d of ds) expect(r.body, s).toContain(d)
    }
  })

  it("makes the spinner an inline block", () => {
    expect(one(".spin").body).toContain("display: inline-block")
  })

  it("opens the Waiting tray at the top from 600 px up", () => {
    const tray = one(".ov.scrim.tray-ov", "@media (min-width: 600px)")
    for (const d of [
      "align-items: flex-start",
      "justify-content: flex-start",
      "padding: 48px 12px",
    ])
      expect(tray.body).toContain(d)
    expect(tray.at).toBeGreaterThan(one(".ov.scrim").at)
    expect(tray.at).toBeLessThan(comps.indexOf(MARK))
  })
})

// Returns the first rule of components.css with the selector s in the media block, or undefined.
const first = (s: string, media = "") =>
  rules(comps).find((r) => r.media === media && r.selectors.includes(s))

describe("phone sheet, card wrap, rail, bar, and tile rules", () => {
  it("resets the button rows of the More sheet and pads the eyebrows", () => {
    const li = first("button.li")
    for (const d of [
      "background: transparent",
      "border: 0",
      "border-bottom: 1px solid var(--border)",
      "color: var(--text)",
      "width: 100%",
    ])
      expect(li?.body, d).toContain(d)
    expect(first(".list > .eyebrow")?.body).toContain("padding: 12px 14px 4px")
  })

  it("wraps the title of a cell row in a card list", () => {
    const r = first("table.rt-cards .cellrow > a")
    expect(r?.selectors).toContain("table.rt-cards .cellrow > b")
    for (const d of ["white-space: normal", "min-width: 0", "overflow-wrap: anywhere"])
      expect(r?.body, d).toContain(d)
  })

  it("lets a rail label wrap to two lines from 1024 px up", () => {
    const r = first(".rail-item .truncate", "@media (min-width: 1024px)")
    for (const d of [
      "display: -webkit-box",
      "-webkit-line-clamp: 2",
      "-webkit-box-orient: vertical",
      "overflow: hidden",
      "white-space: normal",
    ])
      expect(r?.body, d).toContain(d)
    const item = first(".rail-item")
    expect(item?.body).toContain("min-height: 34px")
    expect(item?.body).not.toMatch(/(^|;)\s*height: 34px/)
  })

  it("resets the button style of a usage bar", () => {
    const bar = first(".sbar")
    for (const d of ["padding: 0", "border: 0", "background: none"])
      expect(bar?.body, d).toContain(d)
  })

  it("draws the brand tile at 24 px with a 24 px SVG", () => {
    for (const s of [".rail-mark", ".rail-mark svg"]) {
      expect(first(s)?.body, s).toContain("width: 24px")
      expect(first(s)?.body, s).toContain("height: 24px")
    }
    expect(first(".rail-mark")?.body).toContain("background: var(--text)")
    expect(first(".rail-mark")?.body).toContain("color: var(--bg)")
  })
})

// Returns the rules of components.css that list the selector.
const rulesOf = (s: string) => rules(comps).filter((r) => r.selectors.includes(s))

describe("status bar layout", () => {
  it("scrolls the status items in .sb-scroll, not the whole bar", () => {
    for (const r of rulesOf(".statusbar")) {
      expect(r.body).not.toContain("overflow-x")
      expect(r.body).not.toContain("scrollbar-width")
    }
    expect(rulesOf(".statusbar::-webkit-scrollbar")).toHaveLength(0)
    expect(comps).not.toContain(".sb-spacer")
    const scroll = rulesOf(".sb-scroll").find((r) => r.media === "")
    for (const d of [
      "display: flex",
      "align-items: center",
      "gap: 4px",
      "flex: 1 1 auto",
      "min-width: 0",
      "overflow-x: auto",
      "scrollbar-width: none",
      "padding-block: 4px",
      "margin-block: -4px",
    ])
      expect(scroll?.body, d).toContain(d)
    expect(rulesOf(".sb-scroll::-webkit-scrollbar")[0]?.body).toContain("display: none")
    const fixed = rulesOf(".sb-fixed").find((r) => r.media === "")
    for (const d of ["display: flex", "align-items: center", "gap: 4px", "flex: none"])
      expect(fixed?.body, d).toContain(d)
  })

  it("cuts a long model name with an ellipsis, with a max width on a phone", () => {
    const model = rulesOf(".sb-model").find((r) => r.media === "")
    expect(model?.body).toContain("text-overflow: ellipsis")
    expect(model?.body).toContain("white-space: nowrap")
    const phone = rulesOf(".sb-model").find((r) => r.media === "@media (max-width: 599px)")
    expect(phone?.body).toMatch(/max-width: \d+ch/)
  })

  it("keeps the new status bar rules above the phone tap block", () => {
    for (const s of [".sb-scroll", ".sb-fixed", ".sb-model"])
      for (const r of rulesOf(s)) expect(r.at, s).toBeLessThan(comps.indexOf(MARK))
  })
})

// Returns the joined bodies of the rules that list the selector in the media text.
const bodyAt = (media: string, s: string) =>
  rulesOf(s)
    .filter((r) => r.media === media)
    .map((r) => r.body)
    .join(";")

describe("chat header layout", () => {
  it("keeps the chat header on one row from 600 px and puts the chips last on a phone", () => {
    expect(bodyAt("@media (min-width: 600px)", ".chat-h")).toContain("flex-wrap: nowrap")
    const chips = bodyAt("@media (min-width: 600px)", ".chat-h .chips")
    expect(chips).toContain("flex: 1 1 auto")
    expect(chips).toContain("min-width: 0")
    const end = bodyAt("", ".chat-h-end")
    expect(end).toContain("flex: none")
    expect(end).toContain("margin-left: auto")
    expect(bodyAt("@media (max-width: 599px)", ".chat-h .chips")).toContain("order: 1")
  })
})

describe("Waiting tray rule", () => {
  it("opens the Waiting tray at the top from 600 px with one global rule", () => {
    const top = rules(comps).find(
      (r) => r.media === "@media (min-width: 600px)" && r.selectors.includes(".ov.scrim.tray-ov"),
    )
    expect(top, ".ov.scrim.tray-ov").toBeDefined()
    expect(top!.body).toContain("align-items: flex-start")
    expect(top!.body).toContain("justify-content: flex-start")
    expect(top!.body).toContain("padding: 48px 12px")
    const tray = flat(read("../components/WaitingTray.vue"))
    expect(tray).not.toContain(".tray-ov {")
    expect(tray).toContain('class="ov scrim is-open tray-ov"')
  })
})

describe("usage axis labels", () => {
  it("lets a label grow into the empty slots next to it, and keeps the end labels inside", () => {
    const span = one(".axis-x span").body
    expect(span).not.toContain("overflow: hidden")
    for (const d of ["display: flex", "justify-content: center", "white-space: nowrap"])
      expect(span, d).toContain(d)
    expect(one(".axis-x span:first-child").body).toContain("justify-content: flex-start")
    expect(one(".axis-x span:last-child").body).toContain("justify-content: flex-end")
  })
})

describe("sweep check fixes", () => {
  it("keeps the side focus rings of the status items inside the scroll box", () => {
    const body = bodyAt("", ".sb-scroll")
    expect(body).toContain("padding-inline: 4px")
    expect(body).toContain("margin-inline: -4px")
  })

  it("puts the drawer shadow only on an open drawer from 1024 px to 1439 px", () => {
    const m = "@media (min-width: 1024px) and (max-width: 1439px)"
    expect(bodyAt(m, ".aside-slot > .aside")).toContain("box-shadow: none")
    expect(bodyAt(m, ".aside-slot > .aside.is-open")).toContain("box-shadow: var(--shadow-lg)")
  })

  it("keeps a phone-only menu item a flex row", () => {
    expect(bodyAt("@media (max-width: 599px)", ".menu-item.only-phone")).toContain(
      "display: flex !important",
    )
  })

  it("gives the date axis the padding and the gap of the bars on a phone", () => {
    const m = "@media (max-width: 599px)"
    for (const d of ["padding-left: 34px", "gap: 2px"]) {
      expect(bodyAt(m, ".sbars"), d).toContain(d)
      expect(bodyAt(m, ".axis-x"), d).toContain(d)
    }
  })
})
