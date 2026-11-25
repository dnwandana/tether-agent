// The registry holds each Lucide icon that the glyph map, the tool map, the modes, and the job
// states use, with the component and the SVG node.
import type { Component } from "vue"

import * as AlarmClock from "@lucide/vue/dist/esm/icons/alarm-clock.mjs"
import * as Archive from "@lucide/vue/dist/esm/icons/archive.mjs"
import * as Ban from "@lucide/vue/dist/esm/icons/ban.mjs"
import * as BookOpen from "@lucide/vue/dist/esm/icons/book-open.mjs"
import * as Bot from "@lucide/vue/dist/esm/icons/bot.mjs"
import * as Braces from "@lucide/vue/dist/esm/icons/braces.mjs"
import * as Brain from "@lucide/vue/dist/esm/icons/brain.mjs"
import * as CalendarClock from "@lucide/vue/dist/esm/icons/calendar-clock.mjs"
import * as Camera from "@lucide/vue/dist/esm/icons/camera.mjs"
import * as ChartColumn from "@lucide/vue/dist/esm/icons/chart-column.mjs"
import * as Check from "@lucide/vue/dist/esm/icons/check.mjs"
import * as ChevronLeft from "@lucide/vue/dist/esm/icons/chevron-left.mjs"
import * as ChevronRight from "@lucide/vue/dist/esm/icons/chevron-right.mjs"
import * as CircleCheck from "@lucide/vue/dist/esm/icons/circle-check.mjs"
import * as CircleDollarSign from "@lucide/vue/dist/esm/icons/circle-dollar-sign.mjs"
import * as CircleHelp from "@lucide/vue/dist/esm/icons/circle-question-mark.mjs"
import * as CircleX from "@lucide/vue/dist/esm/icons/circle-x.mjs"
import * as ClipboardList from "@lucide/vue/dist/esm/icons/clipboard-list.mjs"
import * as Code from "@lucide/vue/dist/esm/icons/code.mjs"
import * as Compass from "@lucide/vue/dist/esm/icons/compass.mjs"
import * as CornerDownRight from "@lucide/vue/dist/esm/icons/corner-down-right.mjs"
import * as Dot from "@lucide/vue/dist/esm/icons/dot.mjs"
import * as Eraser from "@lucide/vue/dist/esm/icons/eraser.mjs"
import * as Eye from "@lucide/vue/dist/esm/icons/eye.mjs"
import * as FastForward from "@lucide/vue/dist/esm/icons/fast-forward.mjs"
import * as FileDiff from "@lucide/vue/dist/esm/icons/file-diff.mjs"
import * as FileDown from "@lucide/vue/dist/esm/icons/file-down.mjs"
import * as FilePen from "@lucide/vue/dist/esm/icons/file-pen.mjs"
import * as FileText from "@lucide/vue/dist/esm/icons/file-text.mjs"
import * as FoldVertical from "@lucide/vue/dist/esm/icons/fold-vertical.mjs"
import * as GitBranch from "@lucide/vue/dist/esm/icons/git-branch.mjs"
import * as GitFork from "@lucide/vue/dist/esm/icons/git-fork.mjs"
import * as Globe from "@lucide/vue/dist/esm/icons/globe.mjs"
import * as History from "@lucide/vue/dist/esm/icons/rotate-ccw-clock.mjs"
import * as Hourglass from "@lucide/vue/dist/esm/icons/hourglass.mjs"
import * as Image from "@lucide/vue/dist/esm/icons/image.mjs"
import * as ImagePlus from "@lucide/vue/dist/esm/icons/image-plus.mjs"
import * as Inbox from "@lucide/vue/dist/esm/icons/inbox.mjs"
import * as Info from "@lucide/vue/dist/esm/icons/info.mjs"
import * as KeyRound from "@lucide/vue/dist/esm/icons/key-round.mjs"
import * as Keyboard from "@lucide/vue/dist/esm/icons/keyboard.mjs"
import * as Lightbulb from "@lucide/vue/dist/esm/icons/lightbulb.mjs"
import * as ListChecks from "@lucide/vue/dist/esm/icons/list-checks.mjs"
import * as ListTodo from "@lucide/vue/dist/esm/icons/list-todo.mjs"
import * as Lock from "@lucide/vue/dist/esm/icons/lock.mjs"
import * as Moon from "@lucide/vue/dist/esm/icons/moon.mjs"
import * as MousePointerClick from "@lucide/vue/dist/esm/icons/mouse-pointer-click.mjs"
import * as NotebookPen from "@lucide/vue/dist/esm/icons/notebook-pen.mjs"
import * as Paperclip from "@lucide/vue/dist/esm/icons/paperclip.mjs"
import * as Pause from "@lucide/vue/dist/esm/icons/pause.mjs"
import * as Pencil from "@lucide/vue/dist/esm/icons/pencil.mjs"
import * as Play from "@lucide/vue/dist/esm/icons/play.mjs"
import * as Plug from "@lucide/vue/dist/esm/icons/plug.mjs"
import * as Puzzle from "@lucide/vue/dist/esm/icons/puzzle.mjs"
import * as RefreshCw from "@lucide/vue/dist/esm/icons/refresh-cw.mjs"
import * as RotateCcw from "@lucide/vue/dist/esm/icons/rotate-ccw.mjs"
import * as Save from "@lucide/vue/dist/esm/icons/save.mjs"
import * as ScanText from "@lucide/vue/dist/esm/icons/scan-text.mjs"
import * as ScrollText from "@lucide/vue/dist/esm/icons/scroll-text.mjs"
import * as Search from "@lucide/vue/dist/esm/icons/search.mjs"
import * as Send from "@lucide/vue/dist/esm/icons/send.mjs"
import * as Shield from "@lucide/vue/dist/esm/icons/shield.mjs"
import * as Sparkles from "@lucide/vue/dist/esm/icons/sparkles.mjs"
import * as Square from "@lucide/vue/dist/esm/icons/square.mjs"
import * as SquarePen from "@lucide/vue/dist/esm/icons/square-pen.mjs"
import * as SquareTerminal from "@lucide/vue/dist/esm/icons/square-terminal.mjs"
import * as Target from "@lucide/vue/dist/esm/icons/target.mjs"
import * as Timer from "@lucide/vue/dist/esm/icons/timer.mjs"
import * as TimerOff from "@lucide/vue/dist/esm/icons/timer-off.mjs"
import * as Trash2 from "@lucide/vue/dist/esm/icons/trash.mjs"
import * as TriangleAlert from "@lucide/vue/dist/esm/icons/triangle-exclamation-point.mjs"
import * as Undo2 from "@lucide/vue/dist/esm/icons/undo-2.mjs"
import * as Volume2 from "@lucide/vue/dist/esm/icons/volume-2.mjs"
import * as Wrench from "@lucide/vue/dist/esm/icons/wrench.mjs"
import * as X from "@lucide/vue/dist/esm/icons/x.mjs"
import * as Zap from "@lucide/vue/dist/esm/icons/zap.mjs"

export type IconNode = [tag: string, attrs: Record<string, string | number>][]

type IconModule = { default: Component; __iconData: { node: IconNode } }
// oxlint-disable-next-line no-underscore-dangle
const entry = (m: IconModule) => ({ component: m.default, node: m.__iconData.node })

export const ICONS: Record<string, { component: Component; node: IconNode }> = {
  AlarmClock: entry(AlarmClock),
  Archive: entry(Archive),
  Ban: entry(Ban),
  BookOpen: entry(BookOpen),
  Bot: entry(Bot),
  Braces: entry(Braces),
  Brain: entry(Brain),
  CalendarClock: entry(CalendarClock),
  Camera: entry(Camera),
  ChartColumn: entry(ChartColumn),
  Check: entry(Check),
  ChevronLeft: entry(ChevronLeft),
  ChevronRight: entry(ChevronRight),
  CircleCheck: entry(CircleCheck),
  CircleDollarSign: entry(CircleDollarSign),
  CircleHelp: entry(CircleHelp),
  CircleX: entry(CircleX),
  ClipboardList: entry(ClipboardList),
  Code: entry(Code),
  Compass: entry(Compass),
  CornerDownRight: entry(CornerDownRight),
  Dot: entry(Dot),
  Eraser: entry(Eraser),
  Eye: entry(Eye),
  FastForward: entry(FastForward),
  FileDiff: entry(FileDiff),
  FileDown: entry(FileDown),
  FilePen: entry(FilePen),
  FileText: entry(FileText),
  FoldVertical: entry(FoldVertical),
  GitBranch: entry(GitBranch),
  GitFork: entry(GitFork),
  Globe: entry(Globe),
  History: entry(History),
  Hourglass: entry(Hourglass),
  Image: entry(Image),
  ImagePlus: entry(ImagePlus),
  Inbox: entry(Inbox),
  Info: entry(Info),
  KeyRound: entry(KeyRound),
  Keyboard: entry(Keyboard),
  Lightbulb: entry(Lightbulb),
  ListChecks: entry(ListChecks),
  ListTodo: entry(ListTodo),
  Lock: entry(Lock),
  Moon: entry(Moon),
  MousePointerClick: entry(MousePointerClick),
  NotebookPen: entry(NotebookPen),
  Paperclip: entry(Paperclip),
  Pause: entry(Pause),
  Pencil: entry(Pencil),
  Play: entry(Play),
  Plug: entry(Plug),
  Puzzle: entry(Puzzle),
  RefreshCw: entry(RefreshCw),
  RotateCcw: entry(RotateCcw),
  Save: entry(Save),
  ScanText: entry(ScanText),
  ScrollText: entry(ScrollText),
  Search: entry(Search),
  Send: entry(Send),
  Shield: entry(Shield),
  Sparkles: entry(Sparkles),
  Square: entry(Square),
  SquarePen: entry(SquarePen),
  SquareTerminal: entry(SquareTerminal),
  Target: entry(Target),
  Timer: entry(Timer),
  TimerOff: entry(TimerOff),
  Trash2: entry(Trash2),
  TriangleAlert: entry(TriangleAlert),
  Undo2: entry(Undo2),
  Volume2: entry(Volume2),
  Wrench: entry(Wrench),
  X: entry(X),
  Zap: entry(Zap),
}
