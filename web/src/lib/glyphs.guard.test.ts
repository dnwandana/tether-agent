import { readdirSync, readFileSync } from "node:fs"
import { join, relative, sep } from "node:path"
import { describe, expect, it } from "vitest"
import { graphemes, isMark } from "./glyphs"

// web/src/. import.meta.url is not a file URL in jsdom, so use dirname.
const SRC = join(import.meta.dirname, "..")

// These files hold marks on purpose: the generated API types and the maps of the marks.
const SKIP = new Set(["api/types.gen.ts", "lib/glyphs.ts", "lib/glyphs.json"])

// Returns "line: mark" for each mark in the text. The first line is 1.
function scan(text: string): string[] {
  const hits: string[] = []
  text.split("\n").forEach((line, i) => {
    for (const g of graphemes(line)) if (isMark(g)) hits.push(`${i + 1}: ${g}`)
  })
  return hits
}

// Returns the .ts and .vue files under web/src that the guard reads, relative to web/src.
function sourceFiles(): string[] {
  const found: string[] = []
  const walk = (dir: string) => {
    for (const e of readdirSync(dir, { withFileTypes: true })) {
      const full = join(dir, e.name)
      if (e.isDirectory()) walk(full)
      else if (/\.(ts|vue)$/.test(e.name) && !e.name.endsWith(".test.ts")) {
        const rel = relative(SRC, full).split(sep).join("/")
        if (!SKIP.has(rel)) found.push(rel)
      }
    }
  }
  walk(SRC)
  return found.sort()
}

describe("source guard", () => {
  it("finds each mark and keeps the text characters", () => {
    expect(scan("a ✅ b\nc ✓ d \u26a0\ufe0f e")).toEqual(["1: ✅", "2: ✓", "2: \u26a0\ufe0f"])
    expect(scan("👍🏽 ok\n🇮🇩 👨‍💻")).toEqual(["1: 👍🏽", "2: 🇮🇩", "2: 👨‍💻"])
    expect(scan("‹ Jobs ⋯ • ▾ ▸ ▴ ✕ ✗ ⌛ ● ☐ ✎ ▉ ⊙ ⚿ ↻ ↺ ↳ ◀ ▶")).toEqual([
      "1: ‹",
      "1: ⋯",
      "1: •",
      "1: ▾",
      "1: ▸",
      "1: ▴",
      "1: ✕",
      "1: ✗",
      "1: ⌛",
      "1: ●",
      "1: ☐",
      "1: ✎",
      "1: ▉",
      "1: ⊙",
      "1: ⚿",
      "1: ↻",
      "1: ↺",
      "1: ↳",
      "1: ◀",
      "1: ▶",
    ])
    expect(scan("a · b — c … d → e ← f « g » h ↵ ↑ ↓ © ® ™")).toEqual([])
  })

  it("reads the source files of the page", () => {
    const files = sourceFiles()
    expect(files).toContain("pages/ChatPage.vue")
    expect(files).toContain("lib/command.ts")
    expect(files).not.toContain("lib/glyphs.ts")
    expect(files).not.toContain("api/types.gen.ts")
    expect(files.some((f) => f.endsWith(".test.ts"))).toBe(false)
  })

  it("finds no mark in a source file of the page", () => {
    const hits = sourceFiles().flatMap((f) =>
      scan(readFileSync(join(SRC, f), "utf8")).map((h) => `${f}:${h}`),
    )
    expect(hits).toEqual([])
  })
})
