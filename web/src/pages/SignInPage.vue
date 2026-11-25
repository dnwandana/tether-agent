<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue"
import { ArrowLeftIcon } from "@lucide/vue"
import { useRoute, useRouter } from "vue-router"
import { api, setSessionHeader } from "../api/client"
import type { SignInPoll, SignInStart } from "../api/types.gen"
import BrandMark from "../components/BrandMark.vue"
import SidePanel from "../components/SidePanel.vue"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
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
  <div class="flex min-h-dvh items-center justify-center p-4">
    <Card class="w-full max-w-sm">
      <CardHeader>
        <div class="flex items-center gap-2"><BrandMark /><b class="font-semibold">Tether</b></div>
      </CardHeader>
      <CardContent class="flex flex-col gap-4">
        <template v-if="view === 'start'">
          <h1 class="text-xl font-semibold tracking-tight">Sign in to the dashboard</h1>
          <Alert v-if="route.query.reason === 'ended'">
            <AlertDescription>Your session ended. Request access again.</AlertDescription>
          </Alert>
          <p class="text-muted-foreground">
            Ask for access. An owner approves the request with one tap in Telegram, or on a device
            that is already signed in.
          </p>
          <Button class="h-11 w-full" type="button" :disabled="busy" @click="requestAccess">
            Request access
          </Button>
          <p v-if="errorText" role="alert" class="text-sm text-destructive">{{ errorText }}</p>
          <p>
            <Button variant="link" type="button" @click="show('code')">
              Use a code from tether dashboard link
            </Button>
            <span class="text-sm text-muted-foreground"> Needs a terminal on the host.</span>
          </p>
          <p class="text-sm text-muted-foreground">
            No user names and no passwords. Limit who can reach this address with your VPN or
            firewall.
          </p>
        </template>

        <template v-else-if="view === 'waiting'">
          <Spinner />
          <h1 class="text-xl font-semibold tracking-tight">Sign-in request sent</h1>
          <p class="text-muted-foreground">Check code</p>
          <p class="font-mono text-3xl tracking-widest" aria-live="polite">{{ requestCode }}</p>
          <p class="text-muted-foreground">
            Approve it in Telegram, or on a device that is already signed in.
          </p>
          <p class="text-sm text-muted-foreground">
            Expires in {{ left }}. This page continues by itself.
          </p>
          <p v-if="pollError" role="status" class="text-sm text-warning">{{ pollError }}</p>
          <Button variant="outline" type="button" @click="cancel">Cancel</Button>
        </template>

        <template v-else-if="view === 'denied'">
          <h1 class="text-xl font-semibold tracking-tight">Request denied</h1>
          <p class="text-muted-foreground">An owner tapped Deny. Ask them, then try again.</p>
          <Button type="button" :disabled="busy" @click="requestAccess">
            Request access again
          </Button>
          <p v-if="errorText" role="alert" class="text-sm text-destructive">{{ errorText }}</p>
        </template>

        <template v-else-if="view === 'expired'">
          <h1 class="text-xl font-semibold tracking-tight">Request expired</h1>
          <p class="text-muted-foreground">Nobody answered in 5 minutes.</p>
          <Button type="button" :disabled="busy" @click="requestAccess">
            Request access again
          </Button>
          <p v-if="errorText" role="alert" class="text-sm text-destructive">{{ errorText }}</p>
        </template>

        <template v-else>
          <h1 class="text-xl font-semibold tracking-tight">Sign in with a code</h1>
          <p class="text-muted-foreground">
            On the host, run tether dashboard link. Open the link it prints, or paste its one-time
            code here. The code works once and expires after 5 minutes.
          </p>
          <Input
            v-model="code"
            class="h-11 font-mono"
            aria-label="One-time code"
            autocomplete="one-time-code"
            spellcheck="false"
            @keydown.enter.prevent="!busy && signInWithCode()"
          />
          <p v-if="errorText" role="alert" class="text-sm text-destructive">{{ errorText }}</p>
          <Button type="button" :disabled="busy" @click="signInWithCode">Sign in</Button>
          <Button variant="link" type="button" @click="show('start')">
            <ArrowLeftIcon />
            Request access instead
          </Button>
        </template>

        <p v-if="!secure" class="text-sm text-muted-foreground">
          Voice notes, Copy, and notifications need HTTPS or localhost
          <Button variant="link" type="button" @click="helpOpen = true">
            How to turn on HTTPS
          </Button>
        </p>
      </CardContent>
    </Card>

    <SidePanel :open="helpOpen" title="Turn on HTTPS" @close="helpOpen = false">
      <p>
        Voice notes, Copy, and browser notifications need HTTPS, or a page on localhost. Pick one
        way:
      </p>
      <ol class="flex list-decimal flex-col gap-1 pl-5">
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
