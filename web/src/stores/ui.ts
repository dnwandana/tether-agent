import { reactive, watch } from "vue"

export type ToastKind = "ok" | "info" | "error"
export type Toast = { id: number; text: string; kind: ToastKind }
export type Theme = "system" | "light" | "dark"
export type ConfirmOpts = { title: string; body: string; confirmLabel: string; danger?: boolean }

const TOAST_MS = 6000
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

// At most one toast shows. A new toast replaces the old one.
export const toasts = reactive<Toast[]>([])
let toastId = 0
let toastTimer: ReturnType<typeof setTimeout> | null = null

export function toast(text: string, kind: ToastKind = "info"): void {
  if (toastTimer) clearTimeout(toastTimer)
  toasts.splice(0, toasts.length, { id: ++toastId, text, kind })
  toastTimer = setTimeout(() => {
    toasts.splice(0, toasts.length)
    toastTimer = null
  }, TOAST_MS)
}

const storedTheme = readKey(KEY_THEME)
export const prefs = reactive<{ theme: Theme; sound: boolean; lastRoute: string }>({
  theme: storedTheme === "light" || storedTheme === "dark" ? storedTheme : "system",
  sound: readKey(KEY_SOUND) !== "false",
  lastRoute: readKey(KEY_ROUTE) ?? "",
})

watch(
  prefs,
  (p) => {
    writeKey(KEY_THEME, p.theme)
    writeKey(KEY_SOUND, String(p.sound))
    writeKey(KEY_ROUTE, p.lastRoute)
  },
  { deep: true },
)

let mediaWatched = false

function applyTheme(): void {
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
}

export function setTheme(t: Theme): void {
  prefs.theme = t
  writeKey(KEY_THEME, t)
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
