import { readdirSync, readFileSync } from "node:fs"
import { join, relative } from "node:path"
import { mount } from "@vue/test-utils"
import {
  BellIcon,
  BrainIcon,
  CalendarClockIcon,
  ChartColumnIcon,
  CpuIcon,
  HistoryIcon,
  HourglassIcon,
  KeyRoundIcon,
  MenuIcon,
  MessageSquareIcon,
  MonitorIcon,
  MoonIcon,
  PaletteIcon,
  RocketIcon,
  SearchIcon,
  ServerIcon,
  ShieldCheckIcon,
  SparklesIcon,
  SunIcon,
  TargetIcon,
  WrenchIcon,
} from "@lucide/vue"
import { describe, expect, it } from "vitest"
import * as icons from "./icons"
import { BRAND_PATH, ICONS } from "./icons"

// web/src/. import.meta.url is not a file URL in jsdom, so use dirname.
const SRC = join(import.meta.dirname, "..")

// Returns the .ts and .vue files under web/src that are not tests, relative to it.
function sourceFiles(): string[] {
  const found: string[] = []
  const walk = (dir: string) => {
    for (const e of readdirSync(dir, { withFileTypes: true })) {
      const full = join(dir, e.name)
      if (e.isDirectory()) walk(full)
      else if (/\.(ts|vue)$/.test(e.name) && !e.name.endsWith(".test.ts"))
        found.push(relative(SRC, full))
    }
  }
  walk(SRC)
  return found
}

const LUCIDE = {
  chat: MessageSquareIcon,
  sessions: HistoryIcon,
  jobs: CalendarClockIcon,
  goals: TargetIcon,
  security: ShieldCheckIcon,
  skills: SparklesIcon,
  memory: BrainIcon,
  usage: ChartColumnIcon,
  models: CpuIcon,
  system: ServerIcon,
  access: KeyRoundIcon,
  more: MenuIcon,
  search: SearchIcon,
  sun: SunIcon,
  moon: MoonIcon,
  start: RocketIcon,
  ops: WrenchIcon,
  waiting: HourglassIcon,
  theme: PaletteIcon,
  bell: BellIcon,
  monitor: MonitorIcon,
}

describe("ICONS", () => {
  it("maps each name to its lucide icon", () => {
    expect(ICONS).toEqual(LUCIDE)
    expect(Object.isFrozen(ICONS)).toBe(true)
  })

  it("draws each icon as a hidden inline SVG", () => {
    for (const [name, icon] of Object.entries(ICONS)) {
      const svg = mount(icon).find("svg")
      expect(svg.exists(), name).toBe(true)
      expect(svg.classes(), name).toContain("lucide")
      expect(svg.attributes("aria-hidden"), name).toBe("true")
    }
  })
})

describe("BRAND_PATH", () => {
  it("is the T-hook path of the brand mark", () => {
    expect(BRAND_PATH).toBe("M6.24 7.2H17.76M12 7.2v6.72c0 3.84 3.84 4.56 5.28 1.92")
  })
})

describe("icon exports", () => {
  it("exports only the lucide map and the brand path", () => {
    expect(Object.keys(icons).sort()).toEqual(["BRAND_PATH", "ICONS"])
  })

  it("has no user of the old path map", () => {
    const bad = sourceFiles().filter((f) => /\bICON\b/.test(readFileSync(join(SRC, f), "utf8")))
    expect(bad).toEqual([])
  })
})
