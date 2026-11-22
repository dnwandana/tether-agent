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
import { onSignedOut, setSessionHeader } from "./api/client"
import { openStream } from "./api/stream"
import { applySessionEvents } from "./stores/session"
import { applyTurnEvents } from "./stores/turn"
import { applyCardEvents } from "./stores/cards"
import { applyNoticeEvents } from "./stores/notices"
import { applyStatusEvents, status } from "./stores/status"
import { prefs, setTheme } from "./stores/ui"

setTheme(prefs.theme)

let stop: (() => void) | null = null
let buildId = ""

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
          location.reload()
          return
        }
        buildId = buildId || id
      }
      applyStatusEvents(batch)
      applyCardEvents(batch)
      applyTurnEvents(batch)
      applySessionEvents(batch)
      applyNoticeEvents(batch)
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
