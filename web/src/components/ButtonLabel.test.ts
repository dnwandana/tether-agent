import { render, screen } from "@testing-library/vue"
import { describe, expect, it } from "vitest"
import ButtonLabel from "./ButtonLabel.vue"

const draw = (text: string) => render(ButtonLabel, { props: { text } }).container

describe("ButtonLabel", () => {
  it("shows the Check icon for the current entry", () => {
    const c = draw("✓ gpt-5")
    expect(c.querySelector("svg.lucide-check")).not.toBeNull()
    expect(c.querySelector("svg.lucide-triangle-alert")).toBeNull()
    expect(c.textContent?.trim()).toBe("gpt-5")
  })

  it("shows the warning icon and a word for an entry that is not usable", () => {
    const c = draw("✓ ⚠\uFE0F old-model")
    expect(c.querySelector("svg.lucide-check")).not.toBeNull()
    expect(c.querySelector("svg.lucide-triangle-alert")!.classList).toContain("text-warning")
    expect(c.querySelector(".sr-only")?.textContent).toBe("not usable")
    expect(screen.getByText("old-model")).toBeTruthy()
  })

  it("reads the Check mark at the end of the label", () => {
    const c = draw("🛡 Guarded ✓")
    expect(c.querySelector("svg.lucide-check")).not.toBeNull()
    expect(c.textContent?.trim()).toBe("Guarded")
  })

  it("shows only the word for a label with no state", () => {
    const cases: [string, string][] = [
      ["Next ▶", "Next"],
      ["◀ Prev", "Prev"],
      ["↺ Use the default", "Use the default"],
      ["🗑 Delete", "Delete"],
    ]
    for (const [text, label] of cases) {
      const { container, unmount } = render(ButtonLabel, { props: { text } })
      expect(container.querySelector("svg"), text).toBeNull()
      expect(container.textContent?.trim(), text).toBe(label)
      unmount()
    }
  })
})
