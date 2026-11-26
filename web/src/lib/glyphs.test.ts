import { describe, expect, it } from "vitest"
import data from "./glyphs.json"
import { ICONS } from "./glyphIcons"
import {
  GLYPHS,
  GLYPH_TONE_CLASS,
  MODES,
  buttonLabel,
  graphemes,
  isMark,
  splitGlyph,
  toneOfKind,
  toolIcon,
} from "./glyphs"

// mark, key, Lucide icon, tone, word
const GLYPH_ROWS: [string, string, string, string, string][] = [
  ["✅", "ok", "CircleCheck", "ok", "done"],
  ["✓", "done", "Check", "ok", "passed"],
  ["✔", "finished", "Check", "ok", "finished"],
  ["❌", "denied", "CircleX", "bad", "denied"],
  ["✗", "failed", "X", "bad", "failed"],
  ["⛔", "refused", "Ban", "bad", "refused"],
  ["🚫", "refused", "Ban", "bad", "refused"],
  ["⚠", "warning", "TriangleAlert", "warn", "warning"],
  ["ℹ", "info", "Info", "info", "info"],
  ["⏸", "paused", "Pause", "muted", "paused"],
  ["▶", "resumed", "Play", "muted", "running"],
  ["⏹", "stopped", "Square", "muted", "stopped"],
  ["⏳", "waiting", "Hourglass", "muted", "waiting"],
  ["⌛", "expired", "TimerOff", "warn", "no answer"],
  ["⏱", "timeout", "Timer", "muted", "timeout"],
  ["⏰", "cron", "AlarmClock", "muted", "cron"],
  ["💤", "idle", "Moon", "muted", "idle"],
  ["↪", "steer", "CornerDownRight", "info", "steer"],
  ["⊙", "active", "Target", "info", "active"],
  ["↺", "default", "RotateCcw", "muted", "default"],
  ["⚿", "gate", "ListChecks", "muted", "gate"],
  ["▸", "current", "ChevronRight", "muted", "current"],
  ["◀", "prev", "ChevronLeft", "muted", "previous"],
  ["🛡", "approval", "Shield", "info", "approval"],
  ["🔒", "human-only", "Lock", "warn", "human only"],
  ["🔑", "secret", "KeyRound", "info", "secret"],
  ["📤", "send", "Send", "info", "send"],
  ["❓", "question", "CircleHelp", "info", "question"],
  ["📋", "plan", "ClipboardList", "muted", "plan"],
  ["🔀", "delegate", "GitBranch", "muted", "delegation"],
  ["🤖", "acp", "Bot", "muted", "coding agent"],
  ["💻", "terminal", "SquareTerminal", "muted", "terminal"],
  ["🌐", "browser", "Globe", "muted", "browser"],
  ["🧠", "model", "Brain", "muted", "model"],
  ["💾", "saved", "Save", "ok", "saved"],
  ["🗄", "archived", "Archive", "ok", "archived"],
  ["🗑", "deleted", "Trash2", "muted", "deleted"],
  ["✏", "renamed", "Pencil", "ok", "renamed"],
  ["🆕", "new", "SquarePen", "ok", "new"],
  ["↩", "undo", "Undo2", "ok", "undone"],
  ["🍴", "fork", "GitFork", "ok", "forked"],
  ["🗜", "compacted", "FoldVertical", "muted", "compacted"],
  ["♻", "restarted", "RefreshCw", "muted", "restarted"],
  ["↻", "restarted", "RefreshCw", "muted", "restarted"],
  ["🧩", "skill", "Puzzle", "muted", "skill"],
  ["🗂", "curator", "Archive", "muted", "skill curator"],
  ["📌", "rule", "ListChecks", "info", "rule"],
  ["📖", "help", "BookOpen", "muted", "help"],
  ["📊", "usage", "ChartColumn", "muted", "usage"],
  ["💸", "cost", "CircleDollarSign", "warn", "cost alert"],
  ["📜", "runs", "ScrollText", "muted", "runs"],
  ["📎", "file", "Paperclip", "muted", "file"],
  ["⚡", "full", "Zap", "warn", "full"],
  ["👁", "read-only", "Eye", "muted", "read-only"],
  ["🎯", "goal", "Target", "muted", "goal"],
]

// tool, Lucide icon
const TOOL_ROWS: [string, string][] = [
  ["terminal", "SquareTerminal"],
  ["read_file", "FileText"],
  ["write_file", "FilePen"],
  ["patch", "FileDiff"],
  ["web_search", "Search"],
  ["web_fetch", "FileDown"],
  ["clarify", "CircleHelp"],
  ["memory", "Brain"],
  ["session_search", "History"],
  ["skills_list", "BookOpen"],
  ["skill_view", "BookOpen"],
  ["skill_manage", "NotebookPen"],
  ["delegate_task", "GitBranch"],
  ["acp_delegate", "Bot"],
  ["cronjob", "CalendarClock"],
  ["execute_code", "Code"],
  ["view_image", "Image"],
  ["text_to_speech", "Volume2"],
  ["generate_image", "ImagePlus"],
  ["browser_navigate", "Globe"],
  ["browser_click", "MousePointerClick"],
  ["browser_fill", "Keyboard"],
  ["browser_read", "ScanText"],
  ["browser_take_screenshot", "Camera"],
  ["browser_run_js", "Braces"],
  ["todo_list", "ListTodo"],
]

const TONES = ["ok", "warn", "bad", "info", "muted"]

describe("glyphs.json", () => {
  it("holds each row of the glyph table", () => {
    for (const [mark, key, icon, tone, word] of GLYPH_ROWS) {
      expect((data.glyphs as Record<string, unknown>)[mark], mark).toEqual({
        key,
        icon,
        tone,
        word,
      })
    }
  })

  it("holds each row of the tool table", () => {
    for (const [tool, icon] of TOOL_ROWS) {
      expect((data.tools as Record<string, string>)[tool], tool).toBe(icon)
    }
  })

  it("keys each mark as one grapheme with no U+FE0F, and names known icons and tones", () => {
    for (const [mark, row] of Object.entries(data.glyphs)) {
      expect(mark.includes("️"), mark).toBe(false)
      expect(graphemes(mark), mark).toHaveLength(1)
      expect(ICONS[row.icon], mark).toBeDefined()
      expect(TONES, mark).toContain(row.tone)
      expect(row.word.length, mark).toBeGreaterThan(0)
    }
    for (const [tool, icon] of Object.entries(data.tools)) expect(ICONS[icon], tool).toBeDefined()
  })
})

describe("GLYPHS", () => {
  it("gives each mark its key, icon, node, tone, and word", () => {
    for (const [mark, key, icon, tone, word] of GLYPH_ROWS) {
      const g = GLYPHS[mark]
      expect(g, mark).toBeDefined()
      expect(g.key, mark).toBe(key)
      expect(g.icon, mark).toBe(ICONS[icon].component)
      expect(g.node, mark).toBe(ICONS[icon].node)
      expect(g.tone, mark).toBe(tone)
      expect(g.word, mark).toBe(word)
    }
  })

  it("maps each tone to a token class", () => {
    expect(GLYPH_TONE_CLASS).toEqual({
      ok: "text-success",
      warn: "text-warning",
      bad: "text-destructive",
      info: "text-info",
      muted: "text-muted-foreground",
    })
  })

  it("maps each toast kind to a tone", () => {
    expect(toneOfKind("ok")).toBe("ok")
    expect(toneOfKind("info")).toBe("info")
    expect(toneOfKind("warn")).toBe("warn")
    expect(toneOfKind("error")).toBe("bad")
  })
})

describe("toolIcon and MODES", () => {
  it("gives each tool its icon, and Wrench to an unknown tool", () => {
    for (const [tool, icon] of TOOL_ROWS) expect(toolIcon(tool), tool).toBe(ICONS[icon].component)
    expect(toolIcon("no_such_tool")).toBe(ICONS.Wrench.component)
  })

  it("gives each mode its label and icon", () => {
    expect(MODES).toEqual({
      "read-only": { label: "Read-only", icon: ICONS.Eye.component },
      guarded: { label: "Guarded", icon: ICONS.Shield.component },
      full: { label: "Full", icon: ICONS.Zap.component },
    })
  })
})

describe("graphemes and isMark", () => {
  it("splits a text into graphemes", () => {
    expect(graphemes("a👍🏽b")).toEqual(["a", "👍🏽", "b"])
    expect(graphemes("🇮🇩#️⃣")).toEqual(["🇮🇩", "#️⃣"])
    expect(graphemes("")).toEqual([])
  })

  it("finds each emoji and each text mark", () => {
    const marks = [
      ..."✓ ✗ ✕ ▸ ▾ ▴ ◀ ▶ ↻ ↺ ↳ ⌛ ● ☐ ✎ ▉ ⊙ ⚿ ⋯ ‹ •".split(" "),
      "🦄",
      "⚠",
      "⚠️",
      "⏸",
      "ℹ",
      "↩",
      "↔",
      "👍🏽",
      "🇮🇩",
      "#️⃣",
      "👨‍💻",
      "©️",
      "️",
    ]
    for (const m of marks) expect(isMark(m), m).toBe(true)
  })

  it("keeps punctuation, Kbd keys, and the plain © ® ™", () => {
    const kept = ["·", "—", "…", "→", "←", "«", "»", "↵", "↑", "↓", "©", "®", "™", "–", "›"]
    for (const c of [...kept, "a", "1", " ", "é"]) expect(isMark(c), c).toBe(false)
  })
})

describe("splitGlyph", () => {
  it("returns the glyph of the first grapheme and the rest", () => {
    const cases: [string, string | null, string][] = [
      ["✅ Saved.", "ok", "Saved."],
      ["⚠️ The list changed.", "warning", "The list changed."],
      ["✓ Goal cleared.", "done", "Goal cleared."],
      ["↻ Goal 8/20: x", "restarted", "Goal 8/20: x"],
      ["⊙ Goal (active, …)", "active", "Goal (active, …)"],
      ["🔑 Secret needed: GH_TOKEN", "secret", "Secret needed: GH_TOKEN"],
      ["🦄 x", null, "x"],
      ["👍🏽 x", null, "x"],
      ["No goal set.", null, "No goal set."],
      ["", null, ""],
    ]
    for (const [text, key, rest] of cases) {
      const s = splitGlyph(text)
      expect(s.glyph?.key ?? null, text).toBe(key)
      expect(s.rest, text).toBe(rest)
    }
  })
})

describe("buttonLabel", () => {
  it("removes the marks at both ends and reads the current and warn states", () => {
    const cases: [string, string, boolean, boolean][] = [
      ["✓ gpt-5", "gpt-5", true, false],
      ["✓ ⚠️ old-model", "old-model", true, true],
      ["🛡 Guarded ✓", "Guarded", true, false],
      ["Next ▶", "Next", false, false],
      ["◀ Prev", "Prev", false, false],
      ["↺ Use the default", "Use the default", false, false],
      ["🗑 Delete", "Delete", false, false],
      ["Pause", "Pause", false, false],
    ]
    for (const [text, label, current, warn] of cases) {
      expect(buttonLabel(text), text).toEqual({ label, current, warn })
    }
  })
})
