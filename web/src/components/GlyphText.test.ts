import { render } from "@testing-library/vue"
import { describe, expect, it } from "vitest"
import GlyphText from "./GlyphText.vue"

describe("GlyphText", () => {
  it("shows the icon of the mark in its tone, then the rest", () => {
    const { container } = render(GlyphText, { props: { text: "🔑 Secret needed: GH_TOKEN" } })
    const svgs = container.querySelectorAll("svg")
    expect(svgs).toHaveLength(1)
    expect(svgs[0].classList).toContain("lucide-key-round")
    expect(svgs[0].classList).toContain("text-info")
    expect(svgs[0].getAttribute("aria-hidden")).toBe("true")
    expect(container.textContent?.trim()).toBe("Secret needed: GH_TOKEN")
  })

  it("reads a mark with U+FE0F", () => {
    const { container } = render(GlyphText, { props: { text: "⚠\uFE0F Failing" } })
    const svg = container.querySelector("svg")!
    expect(svg.classList).toContain("lucide-triangle-alert")
    expect(svg.classList).toContain("text-warning")
    expect(container.textContent?.trim()).toBe("Failing")
  })

  it("adds the icon class", () => {
    const { container } = render(GlyphText, { props: { text: "⏸ backup", iconClass: "size-3" } })
    const svg = container.querySelector("svg")!
    expect(svg.classList).toContain("lucide-pause")
    expect(svg.classList).toContain("text-muted-foreground")
    expect(svg.classList).toContain("size-3")
  })

  it("drops an unknown mark and shows no icon", () => {
    const { container } = render(GlyphText, { props: { text: "🦄 x" } })
    expect(container.querySelector("svg")).toBeNull()
    expect(container.textContent?.trim()).toBe("x")
  })

  it("shows a text with no mark as it is", () => {
    const { container } = render(GlyphText, { props: { text: "No goal set." } })
    expect(container.querySelector("svg")).toBeNull()
    expect(container.textContent?.trim()).toBe("No goal set.")
  })

  it("gives a lone icon its word for a screen reader", () => {
    const { container } = render(GlyphText, { props: { text: "⏸" } })
    expect(container.querySelector("svg.lucide-pause")).not.toBeNull()
    expect(container.querySelector(".sr-only")?.textContent).toBe("paused")
  })

  it("adds no word when a text follows the icon", () => {
    const { container } = render(GlyphText, { props: { text: "⏸ backup" } })
    expect(container.querySelector(".sr-only")).toBeNull()
  })

  it("cuts a long text with an ellipsis when truncate is set", () => {
    const { container } = render(GlyphText, { props: { text: "⏳ a long goal", truncate: true } })
    const root = container.firstElementChild!
    expect(root.classList).toContain("max-w-full")
    expect(root.lastElementChild!.classList).toContain("truncate")
    const plain = render(GlyphText, { props: { text: "⏳ a long goal" } }).container
    expect(plain.querySelector(".truncate")).toBeNull()
  })
})
