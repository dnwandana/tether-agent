// ICONS holds the Lucide icons of the pages and the shell.
// BRAND_PATH is the path of the brand mark, which stays a custom SVG.
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
import type { Component } from "vue"

export type IconName =
  | "chat"
  | "sessions"
  | "jobs"
  | "goals"
  | "security"
  | "skills"
  | "memory"
  | "usage"
  | "models"
  | "system"
  | "access"
  | "more"
  | "search"
  | "sun"
  | "moon"
  | "start"
  | "ops"
  | "waiting"
  | "theme"
  | "bell"
  | "monitor"

export const ICONS: Readonly<Record<IconName, Component>> = Object.freeze({
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
})

export const BRAND_PATH = "M6.24 7.2H17.76M12 7.2v6.72c0 3.84 3.84 4.56 5.28 1.92"
