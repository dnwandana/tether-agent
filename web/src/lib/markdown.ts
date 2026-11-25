import { Marked } from "marked"

import {
  GLYPHS,
  GLYPH_TONE_CLASS,
  graphemes,
  splitGlyph,
  type Glyph,
  type GlyphTone,
} from "./glyphs"

function escapeHtml(s: string): string {
  return s
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;")
}

// One private instance, so the global marked never changes.
const instance = new Marked({ gfm: true, breaks: true, async: false }).use({
  renderer: {
    // Raw HTML shows as text.
    html({ text }) {
      return escapeHtml(text)
    },
    // Only http, https, and mailto links stay links, and the file links of the server
    // (/api/files/<id>). Other same-origin routes stay text.
    link({ href, title, tokens }) {
      const inner = this.parser.parseInline(tokens)
      if (/^(https?:|mailto:)/i.test(href.trim()) || /^\/api\/files\/[A-Za-z0-9_-]+$/.test(href)) {
        const t = title ? ` title="${escapeHtml(title)}"` : ""
        return `<a href="${escapeHtml(href)}" target="_blank" rel="noopener noreferrer"${t}>${inner}</a>`
      }
      return inner
    },
    // Only /api/files/<id> stays an image. Other same-origin GET routes would run.
    image({ href, text }) {
      if (/^\/api\/files\/[A-Za-z0-9_-]+$/.test(href)) {
        return `<img src="${href}" alt="${escapeHtml(text)}" loading="lazy">`
      }
      return escapeHtml(text)
    },
  },
})

// Renders agent Markdown to HTML that is safe to put in v-html.
export function renderMarkdown(src: string): string {
  return instance.parse(src ?? "") as string
}

// Renders a Markdown file, for example SKILL.md, to HTML that is safe to put in v-html. A single
// newline does not break the line. The per-call option keeps the rules of the one instance.
export function renderDocument(src: string): string {
  return instance.parse(src ?? "", { breaks: false }) as string
}

const FE0F = "\uFE0F"
const SVG_NS = "http://www.w3.org/2000/svg"
const LETTER = /\p{L}/u
// The elements that end a line of text.
const BLOCKS = "p, li, h1, h2, h3, h4, h5, h6, td, th, blockquote, pre, dd, dt"

// Returns the element that holds the line of a text node, or null at the top level.
const blockOf = (n: Text) => n.parentElement?.closest(BLOCKS) ?? null

// Finds the glyph of a grapheme, or undefined for text and unknown marks.
function glyphOf(g: string): Glyph | undefined {
  return GLYPHS[g.replaceAll(FE0F, "")]
}

// Builds the inline SVG element of a glyph.
function glyphSvg(g: Glyph): SVGElement {
  const svg = document.createElementNS(SVG_NS, "svg")
  const attrs: Record<string, string> = {
    xmlns: SVG_NS,
    width: "1em",
    height: "1em",
    viewBox: "0 0 24 24",
    fill: "none",
    stroke: "currentColor",
    "stroke-width": "2",
    "stroke-linecap": "round",
    "stroke-linejoin": "round",
    "aria-hidden": "true",
    "data-glyph": g.key,
    class: `glyph inline-block align-[-0.125em] ${GLYPH_TONE_CLASS[g.tone]}`,
  }
  for (const [k, v] of Object.entries(attrs)) svg.setAttribute(k, v)
  for (const [tag, a] of g.node) {
    const child = document.createElementNS(SVG_NS, tag)
    for (const [k, v] of Object.entries(a)) {
      if (k !== "key") child.setAttribute(k, String(v))
    }
    svg.appendChild(child)
  }
  return svg
}

// Removes each known glyph and the white space after it from an attribute text.
function stripGlyphs(value: string): string {
  const parts = graphemes(value)
  let out = ""
  for (let i = 0; i < parts.length; i++) {
    if (glyphOf(parts[i]) !== undefined) {
      while (i + 1 < parts.length && parts[i + 1].trim() === "") i++
    } else {
      out += parts[i]
    }
  }
  return out.trim()
}

// Renders a Go text (a command reply, a notice, an ack) to HTML that is safe to put in v-html.
// Each known mark becomes a Lucide icon. Text in code stays as it is.
export function renderSystemText(
  text: string,
  opts: { inline?: boolean; lead?: "icon" | "drop"; kind?: GlyphTone } = {},
): string {
  let src = text ?? ""
  if (opts.lead === "drop" && opts.kind !== undefined) {
    const s = splitGlyph(src.trimStart())
    if (s.glyph !== null && s.glyph.tone === opts.kind) src = s.rest
  }
  const template = document.createElement("template")
  template.innerHTML = renderMarkdown(src)

  const nodes: Text[] = []
  const walker = document.createTreeWalker(template.content, NodeFilter.SHOW_TEXT)
  for (let n = walker.nextNode(); n !== null; n = walker.nextNode()) {
    if (!(n.parentElement?.closest("code, pre") ?? null)) nodes.push(n as Text)
  }
  const texts = nodes.map((n) => n.data)
  const breaks = [...template.content.querySelectorAll("br")]
  // Tells whether a line break or a block end is between two text nodes.
  const lineEnds = (a: Text, b: Text): boolean =>
    blockOf(a) !== blockOf(b) ||
    breaks.some(
      (br) =>
        (a.compareDocumentPosition(br) & Node.DOCUMENT_POSITION_FOLLOWING) !== 0 &&
        (b.compareDocumentPosition(br) & Node.DOCUMENT_POSITION_PRECEDING) !== 0,
    )

  // Returns the first character that is not white space, from the given position to the end of
  // the line.
  const nextChar = (ni: number, from: number): string => {
    for (let i = ni; i < texts.length; i++) {
      if (i > ni && lineEnds(nodes[i - 1], nodes[i])) return ""
      const t = (i === ni ? texts[i].slice(from) : texts[i]).trimStart()
      if (t !== "") return Array.from(t)[0]
    }
    return ""
  }

  nodes.forEach((node, ni) => {
    const parts = graphemes(texts[ni])
    if (!parts.some((g) => glyphOf(g) !== undefined)) return
    const frag = document.createDocumentFragment()
    let buf = ""
    let pos = 0
    for (const g of parts) {
      pos += g.length
      const glyph = glyphOf(g)
      if (glyph === undefined) {
        buf += g
        continue
      }
      if (buf !== "") frag.appendChild(document.createTextNode(buf))
      buf = ""
      frag.appendChild(glyphSvg(glyph))
      if (!LETTER.test(nextChar(ni, pos))) {
        const word = document.createElement("span")
        word.className = "sr-only"
        word.textContent = glyph.word
        frag.appendChild(word)
      }
    }
    if (buf !== "") frag.appendChild(document.createTextNode(buf))
    node.replaceWith(frag)
  })

  for (const el of template.content.querySelectorAll("[title], [alt]")) {
    for (const name of ["title", "alt"]) {
      const v = el.getAttribute(name)
      if (v !== null) el.setAttribute(name, stripGlyphs(v))
    }
  }

  // A link with a glyph and no letter gets a name from its text, with each glyph read as its word.
  for (const a of template.content.querySelectorAll("a")) {
    if (a.querySelector("svg.glyph") === null) continue
    const clone = a.cloneNode(true) as HTMLElement
    clone.querySelectorAll(".sr-only").forEach((e) => e.remove())
    if (LETTER.test(clone.textContent ?? "")) continue
    for (const svg of clone.querySelectorAll("svg.glyph")) {
      const key = svg.getAttribute("data-glyph")
      const word = Object.values(GLYPHS).find((g) => g.key === key)?.word ?? ""
      svg.replaceWith(` ${word} `)
    }
    a.setAttribute("aria-label", (clone.textContent ?? "").replace(/\s+/g, " ").trim())
  }

  if (opts.inline) {
    const kids = [...template.content.childNodes].filter(
      (c) => !(c.nodeType === Node.TEXT_NODE && (c.textContent ?? "").trim() === ""),
    )
    if (kids.length === 1 && kids[0].nodeName === "P") return (kids[0] as HTMLElement).innerHTML
  }
  return template.innerHTML
}
