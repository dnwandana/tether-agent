import { describe, expect, it } from "vitest"
import { renderDocument, renderMarkdown, renderSystemText } from "./markdown"

function dom(html: string) {
  const div = document.createElement("div")
  div.innerHTML = html
  return div
}

describe("renderMarkdown", () => {
  it("renders hostile markdown inert", () => {
    const src = [
      "<script>alert(1)</script>",
      "<img src=x onerror=alert(1)>",
      "[x](javascript:alert(1))",
      "![a](/api/sessions/x/export)",
    ].join("\n\n")
    const out = renderMarkdown(src)
    const d = dom(out)
    expect(out).not.toContain("<script")
    expect(d.querySelector("script")).toBeNull()
    expect(d.querySelector("[onerror]")).toBeNull()
    expect(out).not.toMatch(/href="javascript:/i)
    expect(
      [...d.querySelectorAll("a")].some((a) => /^javascript:/i.test(a.getAttribute("href") ?? "")),
    ).toBe(false)
    expect(d.querySelector('img[src="/api/sessions/x/export"]')).toBeNull()
    expect(out).not.toContain('<img src="/api/sessions/x/export"')
    expect(d.textContent).toContain("<script>alert(1)</script>")
    expect(d.textContent).toContain("<img src=x onerror=alert(1)>")
  })

  it("keeps http, https, and mailto links", () => {
    const d = dom(
      renderMarkdown(
        "[docs](https://example.com/a?b=1) [mail](mailto:o@example.com) [rel](/api/jobs)",
      ),
    )
    const links = [...d.querySelectorAll("a")]
    expect(links.map((a) => a.getAttribute("href"))).toEqual([
      "https://example.com/a?b=1",
      "mailto:o@example.com",
    ])
    expect(links[0].getAttribute("target")).toBe("_blank")
    expect(links[0].getAttribute("rel")).toBe("noopener noreferrer")
    expect(d.textContent).toContain("rel")
  })

  it("keeps a link to a file id, and drops a link to another same-origin route", () => {
    const d = dom(renderMarkdown("[📎 a.png](/api/files/ab12CD-_9) [x](/api/sessions/s1/export)"))
    const links = [...d.querySelectorAll("a")]
    expect(links.map((a) => a.getAttribute("href"))).toEqual(["/api/files/ab12CD-_9"])
    expect(d.textContent).toContain("x")
  })

  it("keeps an image of a file id only", () => {
    const d = dom(renderMarkdown("![shot](/api/files/ab12CD-_9) ![x](https://evil.example/p.png)"))
    const imgs = [...d.querySelectorAll("img")]
    expect(imgs.map((i) => i.getAttribute("src"))).toEqual(["/api/files/ab12CD-_9"])
    expect(imgs[0].getAttribute("alt")).toBe("shot")
    expect(d.textContent).toContain("x")
  })

  it("renders common markdown", () => {
    const out = renderMarkdown("**bold** and `code`\nnext line")
    expect(out).toContain("<strong>bold</strong>")
    expect(out).toContain("<code>code</code>")
    expect(out).toContain("<br>")
  })
})

describe("renderDocument", () => {
  it("keeps a soft line break inside one paragraph", () => {
    const out = renderDocument("You are very good at building.\nKeep the plan short.")
    expect(out).not.toContain("<br>")
    const ps = dom(out).querySelectorAll("p")
    expect(ps).toHaveLength(1)
    expect(ps[0].textContent).toBe("You are very good at building.\nKeep the plan short.")
  })

  it("renders hostile markdown inert, the same as renderMarkdown", () => {
    const src = [
      "<script>alert(1)</script>",
      "<img src=x onerror=alert(1)>",
      "[x](javascript:alert(1))",
      "![a](/api/sessions/x/export)",
    ].join("\n\n")
    const out = renderDocument(src)
    const d = dom(out)
    expect(out).not.toContain("<script")
    expect(d.querySelector("script")).toBeNull()
    expect(d.querySelector("[onerror]")).toBeNull()
    expect(out).not.toMatch(/href="javascript:/i)
    expect(d.querySelector('img[src="/api/sessions/x/export"]')).toBeNull()
    expect(d.textContent).toContain("<script>alert(1)</script>")
    expect(d.textContent).toContain("<img src=x onerror=alert(1)>")
  })

  it("keeps the link and image rules of renderMarkdown", () => {
    const d = dom(
      renderDocument(
        "[docs](https://example.com/a?b=1) [rel](/api/jobs) ![shot](/api/files/ab12CD-_9) ![x](https://evil.example/p.png)",
      ),
    )
    const links = [...d.querySelectorAll("a")]
    expect(links.map((a) => a.getAttribute("href"))).toEqual(["https://example.com/a?b=1"])
    expect(links[0].getAttribute("rel")).toBe("noopener noreferrer")
    const imgs = [...d.querySelectorAll("img")]
    expect(imgs.map((i) => i.getAttribute("src"))).toEqual(["/api/files/ab12CD-_9"])
    expect(d.textContent).toContain("rel")
  })

  it("does not change the line breaks of renderMarkdown", () => {
    renderDocument("a\nb")
    expect(renderMarkdown("a\nb")).toContain("<br>")
  })
})

const glyphs = (d: HTMLElement) =>
  [...d.querySelectorAll("svg.glyph")].map((s) => s.getAttribute("data-glyph"))
const words = (d: HTMLElement) => [...d.querySelectorAll(".sr-only")].map((s) => s.textContent)

describe("renderSystemText", () => {
  it("draws a known mark at the start and in the middle of a line", () => {
    const d = dom(renderSystemText("✅ Saved. Job x ⏸ now"))
    expect(glyphs(d)).toEqual(["ok", "paused"])
    expect(d.textContent).not.toContain("✅")
    expect(d.textContent).not.toContain("⏸")
    expect(d.textContent).toContain("Saved. Job x")
    expect(words(d)).toEqual([])
  })

  it("gives the icon its tone, its size, and its Lucide node", () => {
    const svg = dom(renderSystemText("✅ Saved.")).querySelector("svg.glyph")!
    expect(svg.getAttribute("aria-hidden")).toBe("true")
    expect(svg.getAttribute("width")).toBe("1em")
    expect(svg.getAttribute("height")).toBe("1em")
    expect(svg.getAttribute("viewBox")).toBe("0 0 24 24")
    expect(svg.getAttribute("stroke")).toBe("currentColor")
    expect(svg.classList).toContain("text-success")
    expect([...svg.children].map((c) => c.tagName.toLowerCase())).toEqual(["circle", "path"])
    expect(svg.querySelector("[key]")).toBeNull()
  })

  it("reads a mark with U+FE0F as its key", () => {
    const d = dom(renderSystemText("⚠\uFE0F The list changed."))
    expect(glyphs(d)).toEqual(["warning"])
    expect(d.textContent).not.toContain("\uFE0F")
    expect(d.querySelector("svg.glyph")!.classList).toContain("text-warning")
  })

  it("adds the word of a mark that has no letter after it", () => {
    const d = dom(renderSystemText("Last 7: ✓✓✗"))
    expect(glyphs(d)).toEqual(["done", "done", "failed"])
    expect(words(d)).toEqual(["passed", "passed", "failed"])
  })

  it("adds no word when a letter follows in the next element", () => {
    expect(words(dom(renderSystemText("⏳ **Busy**")))).toEqual([])
  })

  it("adds the word of a mark at the end of a line or a block", () => {
    expect(words(dom(renderSystemText("Status: ✓\nRunning now")))).toEqual(["passed"])
    expect(words(dom(renderSystemText("Status: ✓\n\nRunning now")))).toEqual(["passed"])
  })

  it("keeps each character inside code", () => {
    const d = dom(renderSystemText("Run `✅ rm -rf build/` now.\n\n```\n✅ rm -rf build/\n```"))
    expect(d.querySelector("p code")?.textContent).toBe("✅ rm -rf build/")
    expect(d.querySelector("pre")?.textContent).toContain("✅ rm -rf build/")
    expect(glyphs(d)).toEqual([])
  })

  it("keeps raw HTML escaped", () => {
    const d = dom(renderSystemText("<b>x</b> ✅ <script>alert(1)</script>"))
    expect(d.querySelector("b")).toBeNull()
    expect(d.querySelector("script")).toBeNull()
    expect(d.textContent).toContain("<b>x</b>")
    expect(glyphs(d)).toEqual(["ok"])
  })

  it("labels a link that has only a mark, and removes the marks from title and alt", () => {
    const only = dom(renderSystemText("[✅](https://example.com)")).querySelector("a")!
    expect(only.getAttribute("aria-label")).toBe("done")
    const a = dom(renderSystemText('[docs](https://example.com "⚠\uFE0F old docs")')).querySelector(
      "a",
    )!
    expect(a.getAttribute("title")).toBe("old docs")
    expect(a.hasAttribute("aria-label")).toBe(false)
    const img = dom(renderSystemText("![✅ chart](/api/files/abc)")).querySelector("img")!
    expect(img.getAttribute("alt")).toBe("chart")
  })

  it("gives no label to a link with no mark, and keeps the digits of a link label", () => {
    const num = dom(renderSystemText("[42](https://example.com)")).querySelector("a")!
    expect(num.hasAttribute("aria-label")).toBe(false)
    const mixed = dom(renderSystemText("[1 ✅](https://example.com)")).querySelector("a")!
    expect(mixed.getAttribute("aria-label")).toBe("1 done")
  })

  it("removes a single wrapping paragraph with inline", () => {
    const one = dom(renderSystemText("✅ **Saved**", { inline: true }))
    expect(one.querySelector("p")).toBeNull()
    expect(one.querySelector("strong")?.textContent).toBe("Saved")
    expect(glyphs(one)).toEqual(["ok"])
    expect(dom(renderSystemText("a\n\nb", { inline: true })).querySelectorAll("p")).toHaveLength(2)
  })

  it("drops a lead mark only when its tone is the kind", () => {
    const drop = (text: string, kind: "ok" | "warn" | "bad" | "info") =>
      dom(renderSystemText(text, { lead: "drop", kind }))
    const warn = drop("⚠\uFE0F The list changed.", "warn")
    expect(glyphs(warn)).toEqual([])
    expect(warn.textContent?.trim()).toBe("The list changed.")
    expect(glyphs(drop("✅ Saved.", "ok"))).toEqual([])
    expect(glyphs(drop("❌ Denied.", "bad"))).toEqual([])
    expect(glyphs(drop("⏸ Paused job x", "info"))).toEqual(["paused"])
    expect(glyphs(drop("Saved ✅", "ok"))).toEqual(["ok"])
    expect(glyphs(dom(renderSystemText("✅ Saved.", { kind: "ok" })))).toEqual(["ok"])
  })

  it("keeps an unknown mark and the plain © ® ™", () => {
    const text = "🧑\u200D💻 Build done 🚀 · © 2026 Acme 🇮🇩 👨\u200D💻 ® ™"
    const d = dom(renderSystemText(text))
    expect(glyphs(d)).toEqual([])
    expect(d.textContent?.trim()).toBe(text)
  })
})
