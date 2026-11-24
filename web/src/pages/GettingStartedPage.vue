<script setup lang="ts">
import { computed, ref } from "vue"
import { useRouter } from "vue-router"
import { api } from "../api/client"
import type { SetupStep } from "../api/types.gen"
import LiveHeader from "../components/LiveHeader.vue"
import SidePanel from "../components/SidePanel.vue"
import StateBlock from "../components/StateBlock.vue"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import { cn } from "@/lib/utils"
import { friendlyError } from "../lib/errors"
import { useLiveData } from "../lib/live"
import { isSecure, toast } from "../stores/ui"

const router = useRouter()
const state = ref<"loading" | "ready" | "error">("loading")
const errorText = ref("")
const steps = ref<SetupStep[]>([])
const unknownCount = computed(() => steps.value.filter((s) => s.state === "unknown").length)
const helpOpen = ref(false)
const host = location.hostname

async function load(): Promise<void> {
  try {
    steps.value = await api<SetupStep[]>("GET", "/api/setup")
    state.value = "ready"
  } catch (e) {
    errorText.value = friendlyError(e)
    state.value = "error"
  }
}

const { stale, updatedAt, refresh } = useLiveData(load)

type StepTone = "done" | "unknown" | "on"
// Maps a step tone to the colors of its mark.
const STEP_TONE: Readonly<Record<StepTone, string>> = {
  done: "bg-success/15 text-success",
  unknown: "bg-warning/15 text-warning",
  on: "bg-primary text-primary-foreground",
}

// Returns the text and the tone of the mark of a step.
function mark(s: SetupStep, i: number): { text: string; tone: StepTone } {
  switch (s.state) {
    case "done":
      return { text: "✓", tone: "done" }
    case "unknown":
      return { text: "?", tone: "unknown" }
    case "open":
      return { text: String(i + 1), tone: "on" }
  }
}

const busy = ref(false)

async function runDoctor(): Promise<void> {
  if (busy.value) return
  busy.value = true
  try {
    await api("POST", "/api/doctor")
  } catch (e) {
    toast(friendlyError(e), "error")
  }
  try {
    await router.push({ name: "system" })
  } finally {
    busy.value = false
  }
}

async function copy(text: string): Promise<void> {
  try {
    await navigator.clipboard.writeText(text)
    toast("Copied.")
  } catch (e) {
    toast(friendlyError(e), "error")
  }
}
</script>

<template>
  <div class="mx-auto flex w-full max-w-6xl flex-col gap-5 p-4 md:p-6">
    <div class="flex flex-wrap items-center gap-3">
      <h1 class="text-xl font-semibold tracking-tight">Getting started</h1>
      <LiveHeader :updated-at="updatedAt" :stale="stale" @refresh="refresh" />
      <Button variant="outline" type="button" @click="refresh">Check again</Button>
    </div>
    <p class="text-muted-foreground">
      The setup runs in a terminal on the host. This page shows the state of each step and the
      command for an open step.
    </p>

    <StateBlock v-if="state === 'loading'" state="loading" />
    <StateBlock
      v-else-if="state === 'error'"
      state="error"
      :text="errorText"
      action="Retry"
      @action="load"
    />
    <StateBlock v-else-if="steps.length === 0" state="empty" text="No setup steps" />
    <template v-else>
      <div v-if="unknownCount > 0" class="flex flex-wrap items-center gap-3">
        <span class="text-muted-foreground"
          >{{ unknownCount }} {{ unknownCount === 1 ? "step has" : "steps have" }} no doctor
          result.</span
        >
        <Button variant="outline" type="button" :disabled="busy" @click="runDoctor">
          Run doctor
        </Button>
      </div>
      <Card>
        <CardContent>
          <ol aria-label="Setup steps" class="flex flex-col divide-y">
            <li v-for="(s, i) in steps" :key="s.name" class="flex flex-wrap items-start gap-3 py-3">
              <span
                :data-tone="mark(s, i).tone"
                :class="
                  cn(
                    'flex size-6 shrink-0 items-center justify-center rounded-full text-xs font-medium',
                    STEP_TONE[mark(s, i).tone],
                  )
                "
                >{{ mark(s, i).text }}</span
              >
              <span class="flex min-w-0 flex-1 flex-col items-start gap-1">
                <b>{{ s.name }}</b>
                <span class="text-muted-foreground">{{ s.detail }}</span>
                <Button
                  v-if="s.state === 'open' && s.name === 'HTTPS'"
                  variant="link"
                  type="button"
                  @click="helpOpen = true"
                >
                  How to turn on HTTPS
                </Button>
              </span>
              <template v-if="s.state === 'open' && s.command">
                <code class="font-mono text-xs">{{ s.command }}</code>
                <Button
                  v-if="isSecure()"
                  variant="outline"
                  size="sm"
                  type="button"
                  @click="copy(s.command)"
                >
                  Copy
                </Button>
                <p v-else-if="s.name !== 'HTTPS'" class="text-sm text-muted-foreground">
                  Copy needs HTTPS or localhost.
                  <Button variant="link" type="button" @click="helpOpen = true">
                    How to turn on HTTPS
                  </Button>
                </p>
              </template>
            </li>
          </ol>
        </CardContent>
      </Card>
    </template>

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
