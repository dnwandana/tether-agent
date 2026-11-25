import { Marked } from "marked"

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
