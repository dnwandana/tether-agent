// Maps each mark that Go text sends, each tool, and each mode to a Lucide icon, a tone, and a word.
import type { Component } from "vue"

import data from "./glyphs.json"
import { ICONS, type IconNode } from "./glyphIcons"

// Names the tone of a glyph. The tone picks a color token.
export type GlyphTone = "ok" | "warn" | "bad" | "info" | "muted"

// Holds the icon, the tone, and the word of one mark.
export type Glyph = { key: string; icon: Component; node: IconNode; tone: GlyphTone; word: string }

type Row = { key: string; icon: string; tone: GlyphTone; word: string }
const table = data as unknown as { glyphs: Record<string, Row>; tools: Record<string, string> }

const FE0F = "\uFE0F"

// Maps each mark, with no U+FE0F, to its glyph.
export const GLYPHS: Record<string, Glyph> = Object.fromEntries(
  Object.entries(table.glyphs).map(([mark, r]) => [
    mark,
    {
      key: r.key,
      icon: ICONS[r.icon].component,
      node: ICONS[r.icon].node,
      tone: r.tone,
      word: r.word,
    },
  ]),
)

// Maps each tone to its color token class.
export const GLYPH_TONE_CLASS: Record<GlyphTone, string> = {
  ok: "text-success",
  warn: "text-warning",
  bad: "text-destructive",
  info: "text-info",
  muted: "text-muted-foreground",
}

// Maps each security mode to its label and icon.
export const MODES: Record<string, { label: string; icon: Component }> = {
  "read-only": { label: "Read-only", icon: ICONS.Eye.component },
  guarded: { label: "Guarded", icon: ICONS.Shield.component },
  full: { label: "Full", icon: ICONS.Zap.component },
}

// Returns the icon of a tool, or the Wrench icon for an unknown tool.
export function toolIcon(name: string): Component {
  return ICONS[table.tools[name] ?? "Wrench"].component
}

const segmenter = new Intl.Segmenter(undefined, { granularity: "grapheme" })

// Splits a text into graphemes.
export function graphemes(text: string): string[] {
  return Array.from(segmenter.segment(text), (s) => s.segment)
}

const TEXT_MARKS = new Set("✓ ✗ ✕ ▸ ▾ ▴ ◀ ▶ ↻ ↺ ↳ ⌛ ● ☐ ✎ ▉ ⊙ ⚿ ⋯ ‹ •".split(" "))
const EMOJI = new RegExp("^\\p{RGI_Emoji}$", "v")
const PICTOGRAPHIC = /^\p{Extended_Pictographic}$/u

// Tells whether a code point is part of an emoji sequence.
function isEmojiPart(ch: string): boolean {
  const cp = ch.codePointAt(0) ?? 0
  return (
    PICTOGRAPHIC.test(ch) ||
    cp === 0xfe0f ||
    cp === 0x200d ||
    cp === 0x20e3 ||
    (cp >= 0x1f3fb && cp <= 0x1f3ff) ||
    (cp >= 0x1f1e6 && cp <= 0x1f1ff)
  )
}

// Tells whether a grapheme is an emoji, a part of an emoji sequence, or a listed text mark.
export function isMark(grapheme: string): boolean {
  if (grapheme === "©" || grapheme === "®" || grapheme === "™") return false
  if (TEXT_MARKS.has(grapheme.replaceAll(FE0F, ""))) return true
  return EMOJI.test(grapheme) || Array.from(grapheme).some(isEmojiPart)
}

// Splits the first mark off a text and returns its glyph, or null for an unknown mark.
export function splitGlyph(text: string): { glyph: Glyph | null; rest: string } {
  const first = graphemes(text)[0]
  if (first === undefined) return { glyph: null, rest: text }
  const glyph = GLYPHS[first.replaceAll(FE0F, "")]
  if (glyph === undefined && !isMark(first)) return { glyph: null, rest: text }
  return { glyph: glyph ?? null, rest: text.slice(first.length).trimStart() }
}

const isSpace = (g: string) => g.trim() === ""

// Removes the marks at both ends of a button text and reads the current and warn states.
export function buttonLabel(text: string): { label: string; current: boolean; warn: boolean } {
  const parts = graphemes(text.trim())
  let current = false
  let warn = false
  const note = (g: string) => {
    const bare = g.replaceAll(FE0F, "")
    if (bare === "✓") current = true
    if (bare === "⚠") warn = true
  }
  for (;;) {
    while (parts.length > 0 && isSpace(parts[0])) parts.shift()
    while (parts.length > 0 && isSpace(parts[parts.length - 1])) parts.pop()
    if (parts.length > 0 && isMark(parts[0])) note(parts.shift() as string)
    else if (parts.length > 0 && isMark(parts[parts.length - 1])) note(parts.pop() as string)
    else break
  }
  return { label: parts.join(""), current, warn }
}

// Maps a toast kind to a glyph tone.
export function toneOfKind(kind: "ok" | "info" | "warn" | "error"): GlyphTone {
  return kind === "error" ? "bad" : kind
}
