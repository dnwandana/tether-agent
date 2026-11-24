// @vitest-environment node
import { readdirSync, readFileSync } from "node:fs"
import { join } from "node:path"
import { fileURLToPath } from "node:url"
import { describe, expect, it } from "vitest"

const read = (f: string) => readFileSync(new URL(f, import.meta.url), "utf8")
const app = read("./app.css")
const typeset = read("./typeset.css")
const main = read("../main.ts")

// Prettier formats the CSS. The checks remove the comments and collapse the white space, so a
// change of the layout does not fail them.
const flat = (css: string) =>
  css
    .replace(/\/\*[\s\S]*?\*\//g, "")
    .replace(/\s+/g, " ")
    .trim()

const esc = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")

// Returns the body of each block with the head, at any depth, without the braces.
function blocks(css: string, head: string): string[] {
  const s = flat(css)
  const re = new RegExp("(?:^|[{};])\\s*" + esc(head) + "\\s*\\{", "g")
  const out: string[] = []
  for (const m of s.matchAll(re)) {
    let i = m.index! + m[0].length
    const start = i
    for (let depth = 1; depth > 0; i++) {
      if (s[i] === "{") depth++
      else if (s[i] === "}") depth--
    }
    out.push(s.slice(start, i - 1).trim())
  }
  return out
}

// Returns the declarations of the first block with the head, as a sorted list.
function decls(css: string, head: string): string[] {
  const all = blocks(css, head)
  expect(all.length, head).toBeGreaterThan(0)
  return all[0]
    .split(";")
    .map((d) => d.trim())
    .filter(Boolean)
    .sort()
}

describe("app.css base", () => {
  it("imports Tailwind, the animations, Typeset, and the toast styles first, in this order", () => {
    const s = flat(app)
    const at = [
      '@import "tailwindcss";',
      '@import "tw-animate-css";',
      '@import "./typeset.css";',
      '@import "vue-sonner/style.css";',
    ].map((x) => s.indexOf(x))
    expect(
      at.every((i) => i >= 0),
      String(at),
    ).toBe(true)
    expect([...at].sort((a, b) => a - b)).toEqual(at)
    expect(at[3]).toBeLessThan(s.indexOf("{"))
  })

  it("leaves the test files out of the class scan", () => {
    // The flat() helper reads "/**/" inside the string as a comment, so this check uses the raw text.
    expect(app).toContain('@source not "../../**/*.test.ts";')
  })

  it("keeps the Tailwind breakpoints and adds tab and wide", () => {
    expect(decls(app, "@theme")).toEqual(["--breakpoint-tab: 37.5rem", "--breakpoint-wide: 90rem"])
    expect(app).not.toMatch(/--breakpoint-(\*|sm|md|lg|xl|2xl)\s*:/)
  })

  it("keeps the layout of the page root", () => {
    expect(decls(app, "html, body")).toEqual(["height: 100%", "margin: 0"])
    expect(blocks(app, "body").some((b) => b.includes("overflow: hidden"))).toBe(true)
    expect(decls(app, "#app")).toEqual([
      "display: flex",
      "flex-direction: column",
      "height: 100dvh",
      "height: 100vh",
      "padding-top: env(safe-area-inset-top)",
    ])
    expect(decls(app, "[hidden]")).toEqual(["display: none !important"])
  })

  it("keeps the classes that code outside the templates reads", () => {
    expect(decls(app, ".scroll")).toEqual([
      "flex: 1 1 auto",
      "min-height: 0",
      "min-width: 0",
      "overflow-x: hidden",
      "overflow-y: auto",
    ])
    expect(decls(app, ".aside-slot")).toEqual(["display: contents"])
    expect(decls(app, ".announce")).toEqual([
      "border-width: 0",
      "clip-path: inset(50%)",
      "height: 1px",
      "margin: -1px",
      "overflow: hidden",
      "padding: 0",
      "position: absolute",
      "white-space: nowrap",
      "width: 1px",
    ])
    expect(decls(app, ".pulse")).toEqual(["animation: tt-flash 1.2s ease-out 1"])
    expect(blocks(app, "@keyframes tt-flash")[0]).toContain("box-shadow: 0 0 0 3px var(--ring)")
  })

  it("stops each animation and transition under reduced motion", () => {
    const all = blocks(app, "@media (prefers-reduced-motion: reduce)").join(" ")
    for (const d of [
      "animation-duration: 0.01ms !important",
      "animation-iteration-count: 1 !important",
      "transition-duration: 0.01ms !important",
    ]) {
      expect(all).toContain(d)
    }
  })

  it("gives the controls 44 px and 16 px text below 600 px", () => {
    const phone = blocks(app, "@media (width < 37.5rem)").join(" ")
    for (const s of [
      '[data-slot="button"]',
      '[data-slot="dropdown-menu-item"]',
      '[data-slot="dropdown-menu-radio-item"]',
      '[data-slot="dropdown-menu-checkbox-item"]',
      '[data-slot="toggle-group-item"]',
      '[data-slot="tabs-trigger"]',
      'input:not([type="checkbox"], [type="radio"])',
      "textarea",
      "select",
    ]) {
      expect(phone, s).toContain(s)
    }
    expect(phone).toContain("min-height: 44px")
    expect(phone).toContain("font-size: 16px")
  })

  it("has the utilities of the copied chat parts", () => {
    const s = flat(app)
    for (const u of [
      "shimmer",
      "shimmer-none",
      "scroll-fade-x",
      "scroll-fade-y",
      "scroll-fade-b",
      "scrollbar-thumb-transparent",
    ]) {
      expect(s, u).toContain(`@utility ${u} {`)
    }
    for (const k of [
      "tw-shimmer",
      "scroll-fade-reveal-t",
      "scroll-fade-reveal-b",
      "scroll-fade-reveal-s",
      "scroll-fade-reveal-e",
    ]) {
      expect(s, k).toContain(`@keyframes ${k} {`)
    }
    for (const p of [
      "--shimmer-angle",
      "--shimmer-image",
      "--shimmer-text-fill",
      "--scroll-fade-t",
      "--scroll-fade-b",
      "--scroll-fade-s",
      "--scroll-fade-e",
      "--scroll-fade-mask",
    ]) {
      expect(s, p).toContain(`@property ${p} {`)
    }
  })

  it("loads no font and no other network asset", () => {
    for (const css of [app, typeset]) {
      expect(css).not.toContain("@font-face")
      expect(css).not.toContain("url(")
    }
  })
})

describe("typeset.css", () => {
  it("is the Typeset file of shadcn-vue with the system fonts", () => {
    const s = flat(typeset)
    expect(s).toContain(".typeset {")
    expect(s).toContain("--typeset-font-body: inherit")
    expect(s).toContain("--typeset-font-heading: inherit")
    expect(s).toMatch(/--typeset-font-mono: ui-monospace,/)
    expect(s).not.toContain("var(--font-heading)")
    expect(s).not.toContain("var(--font-mono)")
  })

  it("has the chat preset", () => {
    expect(decls(typeset, ".typeset-chat")).toEqual([
      "--typeset-flow: 1em",
      "--typeset-leading: 1.6",
    ])
  })
})

describe("main.ts", () => {
  it("imports app.css and no legacy style file", () => {
    expect(main).toContain('import "./styles/app.css"')
    expect(main).not.toMatch(/styles\/(?:tokens|base|components)\.css/)
  })
})

const SRC = fileURLToPath(new URL("../", import.meta.url)) // web/src/

// Returns the paths under web/src, relative to it, that end in one of the extensions.
function sources(exts: string[], dir = ""): string[] {
  const out: string[] = []
  for (const e of readdirSync(join(SRC, dir), { withFileTypes: true })) {
    if (e.name === "node_modules") continue
    const rel = dir ? `${dir}/${e.name}` : e.name
    if (e.isDirectory()) out.push(...sources(exts, rel))
    else if (exts.some((x) => e.name.endsWith(x))) out.push(rel)
  }
  return out
}
const text = (rel: string) => readFileSync(join(SRC, rel), "utf8")

// Legacy class names that are not also Tailwind utilities. "table", "grow", and "truncate"
// are Tailwind utilities, so the check cannot tell them apart and leaves them out.
const LEGACY_CLASSES = [
  "btn",
  "card",
  "chip",
  "badge",
  "input",
  "seg",
  "ov",
  "scrim",
  "dialog",
  "popover",
  "sheet",
  "t-12",
  "c-2",
  "c-3",
  "w-600",
  "mono",
  "sr",
]

describe("legacy styles", () => {
  it("imports neither components.css nor base.css from a source file", () => {
    const bad = sources([".ts", ".vue"])
      .filter((f) => !f.endsWith(".test.ts"))
      .filter((f) => /["'][^"']*styles\/(?:components|base)\.css["']/.test(text(f)))
    expect(bad).toEqual([])
  })

  it("uses no legacy class in a template", () => {
    const bad: string[] = []
    for (const f of sources([".vue"]).filter((p) => !p.startsWith("components/ui/")))
      for (const m of text(f).matchAll(/\sclass="([^"]*)"/g))
        for (const c of m[1].split(/\s+/)) if (LEGACY_CLASSES.includes(c)) bad.push(`${f}: ${c}`)
    expect(bad).toEqual([])
  })

  it("loads no web font", () => {
    const css = text("styles/app.css") + text("styles/typeset.css")
    expect(css).not.toContain("@font-face")
    expect(css).not.toContain("fonts.googleapis")
  })
})

// An import or a vi.mock of a deleted file.
const DELETED_IMPORT =
  /(?:^\s*import\b[^;]*?|\bvi\.mock\(\s*)["'][^"']*(?:lib\/overlay|ToastHost(?:\.vue)?|styles\/tokens\.css)["']/m
// A custom property of the deleted tokens.css. "--accent-foreground" is a shadcn-vue token,
// so the pattern names only the legacy "--accent-" names.
const LEGACY_TOKEN =
  /var\(--(?:bg|text(?:-[23]|-inv)?|surface-[a-z0-9]+|accent-(?:hover|strong|tint))\)/

describe("legacy code", () => {
  it("imports neither the overlay stack, the toast host, nor tokens.css", () => {
    const bad = sources([".ts", ".vue"])
      .filter((f) => f !== "styles/app.test.ts")
      .filter((f) => DELETED_IMPORT.test(text(f)))
    expect(bad).toEqual([])
  })

  it("uses no custom property of the legacy tokens", () => {
    const bad = sources([".ts", ".vue"])
      .filter((f) => f !== "styles/app.test.ts")
      .filter((f) => LEGACY_TOKEN.test(text(f)))
    expect(bad).toEqual([])
  })

  it("has no legacy token file, overlay stack, or toast host on disk", () => {
    const all = sources([".ts", ".vue", ".css"])
    for (const f of [
      "styles/tokens.css",
      "lib/overlay.ts",
      "lib/overlay.test.ts",
      "components/ToastHost.vue",
      "components/ToastHost.test.ts",
    ])
      expect(all, f).not.toContain(f)
  })
})

const LIGHT = [
  "color-scheme: light",
  "--radius: 0.625rem",
  "--background: oklch(1 0 0)",
  "--foreground: oklch(13% 0.028 261.692)",
  "--card: oklch(1 0 0)",
  "--card-foreground: oklch(13% 0.028 261.692)",
  "--popover: oklch(1 0 0)",
  "--popover-foreground: oklch(13% 0.028 261.692)",
  "--primary: oklch(21% 0.034 264.665)",
  "--primary-foreground: oklch(98.5% 0.002 247.839)",
  "--secondary: oklch(96.7% 0.003 264.542)",
  "--secondary-foreground: oklch(21% 0.034 264.665)",
  "--muted: oklch(96.7% 0.003 264.542)",
  "--muted-foreground: oklch(55.1% 0.027 264.364)",
  "--accent: oklch(96.7% 0.003 264.542)",
  "--accent-foreground: oklch(21% 0.034 264.665)",
  "--destructive: oklch(0.577 0.245 27.325)",
  "--border: oklch(92.8% 0.006 264.531)",
  "--input: oklch(92.8% 0.006 264.531)",
  "--ring: oklch(70.7% 0.022 261.325)",
  "--success: oklch(0.627 0.194 149.214)",
  "--warning: oklch(0.666 0.179 58.318)",
  "--info: oklch(0.546 0.245 262.881)",
]
const DARK = [
  "color-scheme: dark",
  "--background: oklch(13% 0.028 261.692)",
  "--foreground: oklch(98.5% 0.002 247.839)",
  "--card: oklch(21% 0.034 264.665)",
  "--card-foreground: oklch(98.5% 0.002 247.839)",
  "--popover: oklch(21% 0.034 264.665)",
  "--popover-foreground: oklch(98.5% 0.002 247.839)",
  "--primary: oklch(92.8% 0.006 264.531)",
  "--primary-foreground: oklch(21% 0.034 264.665)",
  "--secondary: oklch(27.8% 0.033 256.848)",
  "--secondary-foreground: oklch(98.5% 0.002 247.839)",
  "--muted: oklch(27.8% 0.033 256.848)",
  "--muted-foreground: oklch(70.7% 0.022 261.325)",
  "--accent: oklch(27.8% 0.033 256.848)",
  "--accent-foreground: oklch(98.5% 0.002 247.839)",
  "--destructive: oklch(0.704 0.191 22.216)",
  "--border: oklch(1 0 0 / 10%)",
  "--input: oklch(1 0 0 / 15%)",
  "--ring: oklch(55.1% 0.027 264.364)",
  "--success: oklch(0.792 0.209 151.711)",
  "--warning: oklch(0.828 0.189 84.429)",
  "--info: oklch(0.707 0.165 254.624)",
]

describe("app.css theme", () => {
  it("has the light tokens on :root", () => {
    expect(decls(app, ":root")).toEqual([...LIGHT].sort())
  })

  it("has the dark tokens on .dark", () => {
    expect(decls(app, ".dark")).toEqual([...DARK].sort())
  })

  it("gives the OS dark mode the same tokens when the root has no .light", () => {
    const media = blocks(app, "@media (prefers-color-scheme: dark)").join(" ")
    expect(decls(media, ":root:not(.light)")).toEqual(decls(app, ".dark"))
  })

  it("has a dark variant for the class and for the OS", () => {
    expect(flat(app)).toContain(
      "@custom-variant dark { &:is(.dark *) { @slot; } @media (prefers-color-scheme: dark) { &:is(:root:not(.light) *) { @slot; } } }",
    )
  })

  it("maps the tokens to Tailwind colors and radii", () => {
    const theme = blocks(app, "@theme inline").join(" ")
    for (const c of [
      "background",
      "foreground",
      "card",
      "card-foreground",
      "popover",
      "popover-foreground",
      "primary",
      "primary-foreground",
      "secondary",
      "secondary-foreground",
      "muted",
      "muted-foreground",
      "accent",
      "accent-foreground",
      "destructive",
      "border",
      "input",
      "ring",
      "success",
      "warning",
      "info",
    ]) {
      expect(theme, c).toContain(`--color-${c}: var(--${c});`)
    }
    for (const r of [
      "--radius-sm: calc(var(--radius) - 4px);",
      "--radius-md: calc(var(--radius) - 2px);",
      "--radius-lg: var(--radius);",
      "--radius-xl: calc(var(--radius) + 4px);",
    ]) {
      expect(theme).toContain(r)
    }
  })

  it("has no prototype helper tokens", () => {
    expect(app).not.toMatch(/--(danger|brand)\b/)
  })

  it("sets the border, ring, page, and text colors in the base layer", () => {
    const base = blocks(app, "@layer base").join(" ")
    expect(base).toContain("@apply border-border outline-ring/50;")
    expect(base).toContain("@apply bg-background text-foreground;")
  })

  it("defines each variable that it reads with no fallback", () => {
    const s = flat(app)
    const used = new Set([...s.matchAll(/var\(\s*(--[\w-]+)\s*\)/g)].map((m) => m[1]))
    for (const v of used) {
      const known = s.includes(v + ":") || s.includes("@property " + v + " ") || v === "--spacing"
      expect(known, v).toBe(true)
    }
  })
})
