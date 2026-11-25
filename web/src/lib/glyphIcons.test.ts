import { describe, expect, it } from "vitest"
import { createApp, h, type Component } from "vue"
import { ICONS } from "./glyphIcons"

// The icons of the glyph map, the tool map, the modes, and the job states, and the icons of the
// marks of Go text that the main glyph table does not list.
const NAMES = [
  "AlarmClock",
  "Archive",
  "Ban",
  "BookOpen",
  "Bot",
  "Braces",
  "Brain",
  "CalendarClock",
  "Camera",
  "ChartColumn",
  "Check",
  "ChevronLeft",
  "ChevronRight",
  "CircleCheck",
  "CircleDollarSign",
  "CircleHelp",
  "CircleX",
  "ClipboardList",
  "Code",
  "Compass",
  "CornerDownRight",
  "Dot",
  "Eraser",
  "Eye",
  "FastForward",
  "FileDiff",
  "FileDown",
  "FilePen",
  "FileText",
  "FoldVertical",
  "GitBranch",
  "GitFork",
  "Globe",
  "History",
  "Hourglass",
  "Image",
  "ImagePlus",
  "Inbox",
  "Info",
  "KeyRound",
  "Keyboard",
  "Lightbulb",
  "ListChecks",
  "ListTodo",
  "Lock",
  "Moon",
  "MousePointerClick",
  "NotebookPen",
  "Paperclip",
  "Pause",
  "Pencil",
  "Play",
  "Plug",
  "Puzzle",
  "RefreshCw",
  "RotateCcw",
  "Save",
  "ScanText",
  "ScrollText",
  "Search",
  "Send",
  "Shield",
  "Sparkles",
  "Square",
  "SquarePen",
  "SquareTerminal",
  "Target",
  "Timer",
  "TimerOff",
  "Trash2",
  "TriangleAlert",
  "Undo2",
  "Volume2",
  "Wrench",
  "X",
  "Zap",
]

const SVG_TAGS = ["path", "circle", "rect", "line", "polyline", "polygon", "ellipse"]

// Lucide puts the class lucide-<kebab name> on each icon. A renamed icon gets it through its alias.
const kebab = (n: string) =>
  n
    .replace(/([a-z0-9])([A-Z])/g, "$1-$2")
    .replace(/([a-zA-Z])(\d)/g, "$1-$2")
    .toLowerCase()

function classesOf(c: Component): string[] {
  const el = document.createElement("div")
  const app = createApp({ render: () => h(c) })
  app.mount(el)
  const out = [...(el.querySelector("svg")?.classList ?? [])]
  app.unmount()
  return out
}

describe("ICONS", () => {
  it("holds each icon of the maps", () => {
    for (const n of NAMES) expect(ICONS[n], n).toBeDefined()
  })

  it("draws each icon with its Lucide component", () => {
    for (const n of NAMES) expect(classesOf(ICONS[n].component), n).toContain(`lucide-${kebab(n)}`)
  })

  it("gives each icon its SVG node", () => {
    for (const n of NAMES) {
      const node = ICONS[n].node
      expect(node.length, n).toBeGreaterThan(0)
      for (const [tag, attrs] of node) {
        expect(SVG_TAGS, n).toContain(tag)
        expect(typeof attrs, n).toBe("object")
      }
    }
  })

  it("keeps the node of the Lucide module", () => {
    expect(ICONS.CircleCheck.node).toEqual([
      ["circle", { cx: "12", cy: "12", r: "10", key: "1mglay" }],
      ["path", { d: "m16 9-5.5 5.5L8 12", key: "xofnsj" }],
    ])
  })
})
