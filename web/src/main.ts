import { createApp } from "vue"
import App from "./App.vue"
import "./styles/app.css"
import { router } from "./router"
import StatusBar from "./components/StatusBar.vue"
import NavRail from "./components/NavRail.vue"
import TabBar from "./components/TabBar.vue"
import CommandPalette from "./components/CommandPalette.vue"
import ConfirmDialog from "./components/ConfirmDialog.vue"
import AppToaster from "./components/AppToaster.vue"
import { api, onSignedOut, setSessionHeader } from "./api/client"
import { openStream } from "./api/stream"
import { emitEvents } from "./lib/bus"
import { applySessionEvents } from "./stores/session"
import { applyTurnEvents } from "./stores/turn"
import { applyCardEvents } from "./stores/cards"
import { applyNoticeEvents } from "./stores/notices"
import { applyStatusEvents, status } from "./stores/status"
import { applyTheme } from "./stores/ui"
import { onNewBuild, setVersion } from "./lib/update"
import { setHost } from "./stores/host"
import type { BootstrapView } from "./api/types.gen"

applyTheme()

let stop: (() => void) | null = null
let buildId = ""

// Reads the version of the gateway. After a reload for a new build it says "Updated to vX.Y.Z.".
async function loadVersion(): Promise<void> {
  try {
    const b = await api<BootstrapView>("GET", "/api/bootstrap")
    setVersion(b.version)
    setHost(b.host_zone, b.host_offset_min)
  } catch {
    // A failed read changes nothing.
  }
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

// Vite sends this event when a chunk or its CSS does not load. preventDefault stops Vite from
// throwing the error.
window.addEventListener("vite:preloadError", (e) => {
  e.preventDefault()
  onNewBuild()
})

const app = createApp(App)
app.use(router)
app.component("StatusBar", StatusBar)
app.component("NavRail", NavRail)
app.component("TabBar", TabBar)
app.component("CommandPalette", CommandPalette)
app.component("ConfirmDialog", ConfirmDialog)
app.component("AppToaster", AppToaster)
app.mount("#app")
