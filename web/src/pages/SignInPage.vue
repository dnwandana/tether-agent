<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue"
import { useRoute, useRouter } from "vue-router"
import { api, setSessionHeader } from "../api/client"
import type { SignInPoll, SignInStart } from "../api/types.gen"
import BrandMark from "../components/BrandMark.vue"
import SidePanel from "../components/SidePanel.vue"
import { deviceLabel } from "../lib/browser"
import { friendlyError } from "../lib/errors"
import { isSecure, prefs } from "../stores/ui"

type View = "start" | "waiting" | "denied" | "expired" | "code"

const route = useRoute()
const router = useRouter()
const view = ref<View>("start")
const requestCode = ref("")
const code = ref("")
const errorText = ref("")
const pollError = ref("")
const expiresAt = ref(0)
const left = ref("")
const busy = ref(false)
const helpOpen = ref(false)
const host = location.hostname
const secure = computed(() => isSecure())

let timer: ReturnType<typeof setInterval> | null = null
let tick: ReturnType<typeof setInterval> | null = null
let asking = false

// Sets left to the time to the end of the request as m:ss.
function showLeft(): void {
  const s = Math.max(0, Math.ceil((expiresAt.value - Date.now()) / 1000))
  left.value = `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`
}

function stopPoll(): void {
  if (timer !== null) clearInterval(timer)
  timer = null
  if (tick !== null) clearInterval(tick)
  tick = null
}

// Saves the session header and opens the last page.
async function finish(header: string): Promise<void> {
  stopPoll()
  setSessionHeader(header)
  await router.replace(prefs.lastRoute || "/")
}

async function requestAccess(): Promise<void> {
  if (busy.value) return
  errorText.value = ""
  busy.value = true
  try {
    const r = await api<SignInStart>("POST", "/auth/request", {
      label: deviceLabel(navigator.userAgent),
    })
    requestCode.value = r.code
    pollError.value = ""
    const t = new Date(r.expires).getTime()
    expiresAt.value = Number.isNaN(t) ? Date.now() + 5 * 60_000 : t
    showLeft()
    view.value = "waiting"
    poll(r.id)
  } catch (e) {
    errorText.value = friendlyError(e, "/auth/request")
  } finally {
    busy.value = false
  }
}

// Asks for the state of the request every 2 s while the view is "waiting".
function poll(id: string): void {
  stopPoll()
  tick = setInterval(showLeft, 1000)
  timer = setInterval(async () => {
    if (asking || view.value !== "waiting") return
    asking = true
    try {
      const r = await api<SignInPoll & { header?: string }>("GET", `/auth/request/${id}`)
      if (view.value !== "waiting") return
      pollError.value = ""
      if (r.state === "allowed") await finish(r.session || r.header || "")
      else if (r.state === "denied" || r.state === "expired") {
        stopPoll()
        view.value = r.state
      }
    } catch (e) {
      // The next tick asks again.
      pollError.value =
        e instanceof TypeError
          ? "Cannot reach the gateway. The page tries again."
          : friendlyError(e, `/auth/request/${id}`)
    } finally {
      asking = false
    }
  }, 2000)
}

function cancel(): void {
  stopPoll()
  view.value = "start"
}

async function signInWithCode(): Promise<void> {
  if (busy.value) return
  errorText.value = ""
  busy.value = true
  try {
    const r = await api<SignInPoll & { header?: string }>("POST", "/auth/signin", {
      code: code.value.trim(),
    })
    await finish(r.session || r.header || "")
  } catch (e) {
    errorText.value = friendlyError(e, "/auth/signin")
  } finally {
    busy.value = false
  }
}

function show(v: View): void {
  errorText.value = ""
  view.value = v
}

watch(view, (v) => {
  if (v !== "waiting") stopPoll()
})

onMounted(() => {
  const m = /^#code=(.+)$/.exec(location.hash)
  if (m) {
    code.value = decodeURIComponent(m[1])
    view.value = "code"
    history.replaceState(null, "", location.pathname + location.search)
    void signInWithCode()
  }
})
onBeforeUnmount(stopPoll)
</script>

<template>
  <div class="signin">
    <div class="card">
      <div class="card-b">
        <div class="row" style="margin-bottom: 12px"><BrandMark /><b class="t-16">Tether</b></div>

        <template v-if="view === 'start'">
          <h1 class="t-20">Sign in to the dashboard</h1>
          <div v-if="route.query.reason === 'ended'" class="warn-banner">
            Your session ended. Request access again.
          </div>
          <p class="c-2">
            Ask for access. An owner approves the request with one tap in Telegram, or on a device
            that is already signed in.
          </p>
          <button
            class="btn btn-primary btn-touch btn-block"
            type="button"
            :disabled="busy"
            @click="requestAccess"
          >
            Request access
          </button>
          <p v-if="errorText" class="t-12 c-bad">{{ errorText }}</p>
          <p>
            <button class="link" type="button" @click="show('code')">
              Use a code from tether dashboard link
            </button>
            <span class="c-3 t-12"> Needs a terminal on the host.</span>
          </p>
          <p class="c-3 t-12">
            No user names and no passwords. Limit who can reach this address with your VPN or
            firewall.
          </p>
        </template>

        <template v-else-if="view === 'waiting'">
          <span class="spin" />
          <h1 class="t-20">Sign-in request sent</h1>
          <p class="c-2">Check code</p>
          <p class="check-code mono" aria-live="polite">{{ requestCode }}</p>
          <p class="c-2">Approve it in Telegram, or on a device that is already signed in.</p>
          <p class="c-3 t-12">Expires in {{ left }}. This page continues by itself.</p>
          <p v-if="pollError" class="t-12 c-warn" role="status">{{ pollError }}</p>
          <button class="btn btn-secondary" type="button" @click="cancel">Cancel</button>
        </template>

        <template v-else-if="view === 'denied'">
          <h1 class="t-20">Request denied</h1>
          <p class="c-2">An owner tapped ❌ Deny. Ask them, then try again.</p>
          <button class="btn btn-primary" type="button" :disabled="busy" @click="requestAccess">
            Request access again
          </button>
          <p v-if="errorText" class="t-12 c-bad">{{ errorText }}</p>
        </template>

        <template v-else-if="view === 'expired'">
          <h1 class="t-20">Request expired</h1>
          <p class="c-2">Nobody answered in 5 minutes.</p>
          <button class="btn btn-primary" type="button" :disabled="busy" @click="requestAccess">
            Request access again
          </button>
          <p v-if="errorText" class="t-12 c-bad">{{ errorText }}</p>
        </template>

        <template v-else>
          <h1 class="t-20">Sign in with a code</h1>
          <p class="c-2">
            On the host, run tether dashboard link. Open the link it prints, or paste its one-time
            code here. The code works once and expires after 5 minutes.
          </p>
          <input
            v-model="code"
            class="input input-touch mono"
            aria-label="One-time code"
            autocomplete="one-time-code"
            spellcheck="false"
            @keydown.enter.prevent="!busy && signInWithCode()"
          />
          <p v-if="errorText" class="t-12 c-bad">{{ errorText }}</p>
          <p>
            <button class="btn btn-primary" type="button" :disabled="busy" @click="signInWithCode">
              Sign in
            </button>
          </p>
          <button class="link" type="button" @click="show('start')">
            ← Request access instead
          </button>
        </template>

        <p v-if="!secure" class="c-3 t-12">
          Voice notes, Copy, and notifications need HTTPS or localhost
          <button class="link" type="button" @click="helpOpen = true">How to turn on HTTPS</button>
        </p>
      </div>
    </div>

    <SidePanel :open="helpOpen" title="Turn on HTTPS" @close="helpOpen = false">
      <p>
        Voice notes, Copy, and browser notifications need HTTPS, or a page on localhost. Pick one
        way:
      </p>
      <ol class="steps">
        <li>Cert files. Get a certificate, for example tailscale cert {{ host }}.</li>
        <li>Set dashboard.tls_cert and dashboard.tls_key in config.yaml to the two files.</li>
        <li>Restart the gateway. The dashboard then serves HTTPS on the same address.</li>
      </ol>
      <p>
        Or a TLS proxy. Put Caddy or nginx in front, and set dashboard.public_url to its https
        address.
      </p>
    </SidePanel>
  </div>
</template>

<style scoped>
.check-code {
  font-size: 32px;
  line-height: 40px;
  letter-spacing: 0.08em;
  margin: 4px 0 8px;
}
</style>
