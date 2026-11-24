import { createApp } from "vue"
import App from "./App.vue"
import "./styles/tokens.css"
import "./styles/base.css"
import "./styles/components.css"
import { router } from "./router"
import StatusBar from "./components/StatusBar.vue"
import NavRail from "./components/NavRail.vue"
import TabBar from "./components/TabBar.vue"
import CommandPalette from "./components/CommandPalette.vue"
import ConfirmDialog from "./components/ConfirmDialog.vue"
import ToastHost from "./components/ToastHost.vue"
import { api, onSignedOut, setSessionHeader } from "./api/client"
import { openStream } from "./api/stream"
import { emitEvents } from "./lib/bus"
import { applySessionEvents } from "./stores/session"
import { applyTurnEvents } from "./stores/turn"
import { applyCardEvents } from "./stores/cards"
import { applyNoticeEvents } from "./stores/notices"
import { applyStatusEvents, status } from "./stores/status"
import { applyTheme, toast } from "./stores/ui"
import { anyDirty } from "./lib/dirty"

applyTheme()

let stop: (() => void) | null = null
let buildId = ""
let bannerShown = false
const KEY_DRAFT = "tether.draft"
const KEY_FROM = "tether.updated-from"
let version = ""

// Reads the version of the gateway. After a reload for a new build it says "Updated to vX.Y.Z.".
async function loadVersion(): Promise<void> {
  try {
    const b = await api<{ version: string }>("GET", "/api/bootstrap")
    version = b.version
    let from: string | null = null
    try {
      from = sessionStorage.getItem(KEY_FROM)
      if (from !== null) sessionStorage.removeItem(KEY_FROM)
    } catch {
      from = null
    }
    if (from && version && from !== version) {
      toast("Updated to " + (version.startsWith("v") ? version : "v" + version) + ".", "ok")
    }
  } catch {
    // A failed read changes nothing.
  }
}

// Returns true when the composer keeps a draft.
function hasDraft(): boolean {
  try {
    return !!sessionStorage.getItem(KEY_DRAFT)
  } catch {
    return false
  }
}

// Writes the old version for the "Updated to" toast, then loads the new page files.
function reloadNow(): void {
  try {
    if (version) sessionStorage.setItem(KEY_FROM, version)
  } catch {
    // The toast is lost. The reload still runs.
  }
  location.reload()
}

// Shows "A new version is ready · Reload" once, under the status bar, and waits.
function showUpdateBanner(): void {
  if (bannerShown) return
  bannerShown = true
  const bar = document.createElement("div")
  bar.className = "update-banner"
  bar.setAttribute("role", "status")
  bar.style.cssText =
    "display: flex; align-items: center; justify-content: center; gap: 8px; padding: 6px 12px; background: var(--info-tint); color: var(--text); border-bottom: 1px solid var(--info-line); font-size: 13px"
  bar.append("A new version is ready · ")
  const btn = document.createElement("button")
  btn.className = "btn btn-secondary btn-sm"
  btn.textContent = "Reload"
  btn.addEventListener("click", reloadNow)
  bar.append(btn)
  const root = document.getElementById("app")
  root?.insertBefore(bar, root.firstChild)
}

// Reloads at once, or waits with the banner while a form is dirty or a draft waits.
function onNewBuild(): void {
  if (anyDirty() || hasDraft()) showUpdateBanner()
  else reloadNow()
}

function hasSession(): boolean {
  try {
    return !!localStorage.getItem("tether.session")
  } catch {
    return false
  }
}

// Starts the event stream when a session exists and no stream runs.
function startStream(): void {
  if (stop || !hasSession()) return
  stop = openStream(
    (batch) => {
      for (const e of batch) {
        if (e.name !== "hello") continue
        const id = (e.data as { build_id?: string }).build_id ?? ""
        // A new build id after a restart means new page files: load them.
        if (buildId && id && id !== buildId) {
          onNewBuild()
          return
        }
        if (!buildId) void loadVersion()
        buildId = buildId || id
      }
      applyStatusEvents(batch)
      applyCardEvents(batch)
      applyTurnEvents(batch)
      applySessionEvents(batch)
      applyNoticeEvents(batch)
      emitEvents(batch)
    },
    (s) => {
      status.stream = s
      if (s === "signed-out") signedOut()
    },
  )
}

// Ends the session on this page and shows the sign-in page.
function signedOut(): void {
  stop?.()
  stop = null
  setSessionHeader(null)
  if (router.currentRoute.value.name !== "signin") router.push({ name: "signin" })
}

onSignedOut(signedOut)
router.afterEach((to) => {
  if (to.name !== "signin") startStream()
})

const app = createApp(App)
app.use(router)
app.component("StatusBar", StatusBar)
app.component("NavRail", NavRail)
app.component("TabBar", TabBar)
app.component("CommandPalette", CommandPalette)
app.component("ConfirmDialog", ConfirmDialog)
app.component("ToastHost", ToastHost)
app.mount("#app")
