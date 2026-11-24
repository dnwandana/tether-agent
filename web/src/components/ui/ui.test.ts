import { existsSync, readFileSync, readdirSync } from "node:fs"
import { join } from "node:path"
import { render, screen } from "@testing-library/vue"
import { describe, expect, it } from "vitest"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"

const FOLDERS = [
  "alert",
  "alert-dialog",
  "attachment",
  "badge",
  "bubble",
  "button",
  "card",
  "checkbox",
  "code-block",
  "collapsible",
  "command",
  "dialog",
  "dropdown-menu",
  "empty",
  "field",
  "input",
  "input-group",
  "item",
  "kbd",
  "label",
  "marker",
  "message",
  "message-scroller",
  "native-select",
  "popover",
  "progress",
  "scroll-area",
  "separator",
  "sheet",
  "skeleton",
  "sonner",
  "spinner",
  "switch",
  "table",
  "tabs",
  "textarea",
  "toggle",
  "toggle-group",
  "tooltip",
  "trace",
]
// import.meta.url is not a file: URL in jsdom, so paths use import.meta.dirname.
const uiDir = import.meta.dirname
const src = join(uiDir, "..", "..")

describe("components/ui", () => {
  it("has each planned folder with an index.ts", () => {
    for (const f of FOLDERS) {
      expect(existsSync(join(uiDir, f, "index.ts")), f).toBe(true)
    }
  })

  // The highlight helpers pull the Shiki core into the bundle. The page shows plain code.
  it("imports no highlight helper outside the code block folder", () => {
    const files = (readdirSync(src, { recursive: true }) as string[]).filter(
      (f) =>
        /\.(ts|vue)$/.test(f) &&
        !f.endsWith(".test.ts") &&
        !f.replaceAll("\\", "/").startsWith("components/ui/code-block/"),
    )
    for (const f of files) {
      const text = readFileSync(join(src, f), "utf8")
      expect(text, f).not.toMatch(/\b(useHighlightedCode|loadHighlighter|highlightCode)\b/)
    }
  })

  it("has the hand-written components.json", () => {
    const cfg = JSON.parse(readFileSync(join(uiDir, "..", "..", "..", "components.json"), "utf8"))
    expect(cfg).toMatchObject({
      style: "new-york-v4",
      typescript: true,
      tailwind: { config: "", css: "src/styles/app.css", baseColor: "gray", cssVariables: true },
      aliases: {
        components: "@/components",
        ui: "@/components/ui",
        utils: "@/lib/utils",
        lib: "@/lib",
        composables: "@/composables",
      },
      iconLibrary: "lucide",
    })
  })

  it("renders the copied Button with its slot", () => {
    render(Button, { slots: { default: "Save" } })
    expect(screen.getByRole("button", { name: "Save" }).getAttribute("data-slot")).toBe("button")
  })

  it("merges classes with cn", () => {
    expect(cn("px-2", "px-4")).toBe("px-4")
    expect(cn("a", false, "b")).toBe("a b")
  })
})
