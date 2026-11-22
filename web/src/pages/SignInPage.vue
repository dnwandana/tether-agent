<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue"
import { useRoute, useRouter } from "vue-router"
import { api, ApiError, setSessionHeader } from "../api/client"
import type { SignInPoll, SignInStart } from "../api/types.gen"
import SidePanel from "../components/SidePanel.vue"
import { isSecure, prefs } from "../stores/ui"

type View = "start" | "waiting" | "denied" | "expired" | "code"

const route = useRoute()
const router = useRouter()
const view = ref<View>("start")
const requestCode = ref("")
const code = ref("")
const errorText = ref("")
const busy = ref(false)
const helpOpen = ref(false)
const host = location.hostname
const secure = computed(() => isSecure())

let timer: ReturnType<typeof setInterval> | null = null
let asking = false

// Makes the device label of a user agent, for example "Chrome on Android". The order of the
// checks matters: an Android agent also names Linux, and an iPhone agent also names Mac OS X.
function deviceLabel(ua: string): string {
  let browser = "Browser"
  if (ua.includes("Edg/")) browser = "Edge"
  else if (ua.includes("Firefox/")) browser = "Firefox"
  else if (ua.includes("Chrome/")) browser = "Chrome"
  else if (ua.includes("Safari/")) browser = "Safari"
  let system = "an unknown system"
  if (ua.includes("Android")) system = "Android"
  else if (ua.includes("iPhone")) system = "iPhone"
  else if (ua.includes("iPad")) system = "iPad"
  else if (ua.includes("Mac OS X")) system = "macOS"
  else if (ua.includes("Windows")) system = "Windows"
  else if (ua.includes("Linux")) system = "Linux"
  return `${browser} on ${system}`
}

function stopPoll(): void {
  if (timer !== null) clearInterval(timer)
  timer = null
}

// Saves the session header and opens the last page.
async function finish(header: string): Promise<void> {
  stopPoll()
  setSessionHeader(header)
  await router.replace(prefs.lastRoute || "/")
}

async function requestAccess(): Promise<void> {
  errorText.value = ""
  busy.value = true
  try {
    const r = await api<SignInStart>("POST", "/auth/request", {
      label: deviceLabel(navigator.userAgent),
    })
    requestCode.value = r.code
    view.value = "waiting"
    poll(r.id)
  } catch (e) {
    errorText.value = e instanceof ApiError ? e.message : String(e)
  } finally {
    busy.value = false
  }
}

// Asks for the state of the request every 2 s while the view is "waiting".
function poll(id: string): void {
  stopPoll()
  timer = setInterval(async () => {
    if (asking || view.value !== "waiting") return
    asking = true
    try {
      const r = await api<SignInPoll & { header?: string }>("GET", `/auth/request/${id}`)
      if (view.value !== "waiting") return
      if (r.state === "allowed") await finish(r.session || r.header || "")
      else if (r.state === "denied" || r.state === "expired") {
        stopPoll()
        view.value = r.state
      }
    } catch {
      // The next tick asks again.
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
  errorText.value = ""
  busy.value = true
  try {
    const r = await api<SignInPoll & { header?: string }>("POST", "/auth/signin", {
      code: code.value.trim(),
    })
    await finish(r.session || r.header || "")
  } catch (e) {
    errorText.value =
      e instanceof ApiError && e.status === 429
        ? e.message
        : "The code is not valid, or it expired. Run tether dashboard link again."
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
        <div class="row" style="margin-bottom: 12px"><b class="t-16">Tether</b></div>

        <template v-if="view === 'start'">
          <h2>Sign in to the dashboard</h2>
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
          <h2>Sign-in request sent</h2>
          <p class="c-2">
            Check code {{ requestCode }}. Approve it in Telegram, or on a device that is already
            signed in.
          </p>
          <p class="c-3 t-12">The request expires in 5 minutes. This page continues by itself.</p>
          <button class="btn btn-secondary" type="button" @click="cancel">Cancel</button>
        </template>

        <template v-else-if="view === 'denied'">
          <h2>Request denied</h2>
          <p class="c-2">An owner tapped ❌ Deny. Ask them, then try again.</p>
          <button class="btn btn-primary" type="button" @click="requestAccess">
            Request access again
          </button>
          <p v-if="errorText" class="t-12 c-bad">{{ errorText }}</p>
        </template>

        <template v-else-if="view === 'expired'">
          <h2>Request expired</h2>
          <p class="c-2">Nobody answered in 5 minutes.</p>
          <button class="btn btn-primary" type="button" @click="requestAccess">
            Request access again
          </button>
          <p v-if="errorText" class="t-12 c-bad">{{ errorText }}</p>
        </template>

        <template v-else>
          <h2>Sign in with a code</h2>
          <p class="c-2">
            On the host, run tether dashboard link. Open the link it prints, or paste its one-time
            code here. The code works once and expires after 5 minutes.
          </p>
          <input
            v-model="code"
            class="input input-touch mono"
            autocomplete="one-time-code"
            spellcheck="false"
            @keydown.enter="signInWithCode"
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
