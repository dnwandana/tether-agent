import { describe, expect, it } from "vitest"
import { renderMarkdown } from "./markdown"

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
