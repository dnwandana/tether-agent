import { reactive, ref, watch } from "vue"

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

// The toaster holds all toasts together, because vue-sonner expands the whole stack on hover.
export function holdAllToasts(): void {
  for (const t of toasts) holdToast(t.id)
}

export function releaseAllToasts(): void {
  for (const t of toasts) releaseToast(t.id)
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

// The tokens are oklch, and a browser bar may not read oklch. The meta gets white and the hex
// value of Tailwind gray-950.
export const THEME_COLOR: Readonly<Record<"light" | "dark", string>> = Object.freeze({
  light: "#ffffff",
  dark: "#030712",
})

let mediaWatched = false
// A listener of the OS query increments the counter, so a computed that calls resolvedTheme
// updates on an OS change.
const osTick = ref(0)

// Returns the theme that shows. With no matchMedia, System is light, as in the CSS.
export function resolvedTheme(): "light" | "dark" {
  void osTick.value
  if (prefs.theme !== "system") return prefs.theme
  return globalThis.matchMedia?.("(prefers-color-scheme: dark)")?.matches ? "dark" : "light"
}

export function applyTheme(): void {
  const root = document.documentElement
  root.classList.remove("dark", "light")
  if (prefs.theme !== "system") root.classList.add(resolvedTheme())
  const meta = document.querySelector<HTMLMetaElement>('meta[name="theme-color"]')
  if (meta) meta.content = THEME_COLOR[resolvedTheme()]
  if (!mediaWatched) {
    const mq = globalThis.matchMedia?.("(prefers-color-scheme: dark)")
    if (mq) {
      mediaWatched = true
      mq.addEventListener("change", () => {
        osTick.value++
        if (prefs.theme === "system") applyTheme()
      })
    }
  }
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
// updateReady shows the banner "A new version is ready · Reload".
export const ui = reactive({ trayOpen: false, updateReady: false })
