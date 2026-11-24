import { reactive, watch } from "vue"

export type ToastKind = "ok" | "info" | "warn" | "error"
export type ToastAction = { label: string; data: string }
export type Toast = {
  id: number
  text: string
  kind: ToastKind
  actions: ToastAction[]
  sticky: boolean
  markdown: boolean
}
export type Theme = "system" | "light" | "dark"
// focus picks the first focus. The default is cancel for a danger confirm and ok for the others.
// items show as literal text, one row each. markdown renders the title and the body as Markdown;
// only a gateway reply sets it.
export type ConfirmOpts = {
  title: string
  body: string
  confirmLabel: string
  danger?: boolean
  focus?: "ok" | "cancel"
  items?: string[]
  markdown?: boolean
}

const TOAST_MS = 6000
const TOAST_MAX = 3
const KEY_THEME = "tether.theme"
const KEY_SOUND = "tether.sound"
const KEY_ROUTE = "tether.route"

function readKey(key: string): string | null {
  try {
    return globalThis.localStorage.getItem(key)
  } catch {
    return null
  }
}

function writeKey(key: string, value: string): void {
  try {
    globalThis.localStorage.setItem(key, value)
  } catch {
    // Storage can be blocked. The choice then lasts for this page only.
  }
}

// Up to 3 toasts show. ok and info close after 6 s. warn and error stay until the owner closes them.
export const toasts = reactive<Toast[]>([])
let toastId = 0
const toastTimers = new Map<
  number,
  { timer: ReturnType<typeof setTimeout> | null; left: number; start: number }
>()

function startToastTimer(id: number, ms: number): void {
  const rec = { timer: null as ReturnType<typeof setTimeout> | null, left: ms, start: Date.now() }
  rec.timer = setTimeout(() => closeToast(id), ms)
  toastTimers.set(id, rec)
}

export function closeToast(id: number): void {
  const rec = toastTimers.get(id)
  if (rec?.timer) clearTimeout(rec.timer)
  toastTimers.delete(id)
  const i = toasts.findIndex((t) => t.id === id)
  if (i >= 0) toasts.splice(i, 1)
}

export function holdToast(id: number): void {
  const rec = toastTimers.get(id)
  if (!rec?.timer) return
  clearTimeout(rec.timer)
  rec.timer = null
  rec.left = Math.max(0, rec.left - (Date.now() - rec.start))
}

export function releaseToast(id: number): void {
  const rec = toastTimers.get(id)
  if (!rec || rec.timer) return
  startToastTimer(id, rec.left)
}

export function toast(
  text: string,
  kind: ToastKind = "info",
  opts: { actions?: ToastAction[]; markdown?: boolean } = {},
): void {
  if (toasts.length >= TOAST_MAX) {
    const old = toasts.find((t) => !t.sticky)
    if (old) closeToast(old.id)
  }
  const t: Toast = {
    id: ++toastId,
    text,
    kind,
    actions: opts.actions ?? [],
    sticky: kind === "warn" || kind === "error",
    markdown: opts.markdown === true,
  }
  toasts.push(t)
  if (!t.sticky) startToastTimer(t.id, TOAST_MS)
}

const storedTheme = readKey(KEY_THEME)
export const prefs = reactive<{ theme: Theme; sound: boolean; lastRoute: string }>({
  theme: storedTheme === "light" || storedTheme === "dark" ? storedTheme : "system",
  sound: readKey(KEY_SOUND) !== "false",
  lastRoute: readKey(KEY_ROUTE) ?? "",
})

// One watcher for each key. A deep watch wrote all keys on each route change.
watch(
  () => prefs.theme,
  (v) => writeKey(KEY_THEME, v),
  // A sync watcher writes at once, so setTheme needs no write of its own.
  { flush: "sync" },
)
watch(
  () => prefs.sound,
  (v) => writeKey(KEY_SOUND, String(v)),
)
watch(
  () => prefs.lastRoute,
  (v) => writeKey(KEY_ROUTE, v),
)

let mediaWatched = false

export function applyTheme(): void {
  let name: "light" | "dark" = prefs.theme === "light" ? "light" : "dark"
  if (prefs.theme === "system") {
    const mq = globalThis.matchMedia?.("(prefers-color-scheme: light)")
    name = mq?.matches ? "light" : "dark"
    if (mq && !mediaWatched) {
      mediaWatched = true
      mq.addEventListener("change", () => {
        if (prefs.theme === "system") applyTheme()
      })
    }
  }
  document.documentElement.className = `tt theme-${name}`
  // The browser bar takes the --bg color of the theme, so the color stays in tokens.css.
  const meta = document.querySelector<HTMLMetaElement>('meta[name="theme-color"]')
  if (!meta) return
  const bg = getComputedStyle(document.documentElement).getPropertyValue("--bg").trim()
  if (bg) meta.content = bg
}

export function setTheme(t: Theme): void {
  // The prefs.theme watcher writes the key.
  prefs.theme = t
  applyTheme()
}

// Voice notes, Copy, and notifications need a secure context.
export function isSecure(): boolean {
  return globalThis.isSecureContext === true
}

export const pendingConfirm = reactive<{
  opts: ConfirmOpts | null
  resolve: ((ok: boolean) => void) | null
}>({
  opts: null,
  resolve: null,
})

export function confirm(opts: ConfirmOpts): Promise<boolean> {
  pendingConfirm.resolve?.(false)
  return new Promise<boolean>((done) => {
    pendingConfirm.opts = opts
    pendingConfirm.resolve = (ok: boolean) => {
      pendingConfirm.opts = null
      pendingConfirm.resolve = null
      done(ok)
    }
  })
}

// The open flag of the one WaitingTray in App.vue. The status bar, the tab bar, and the palette set it.
export const ui = reactive({ trayOpen: false })
