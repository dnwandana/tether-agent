<script setup lang="ts">
import { ref } from "vue"
import { useRouter } from "vue-router"
import { api } from "../api/client"
import type { SetupStep } from "../api/types.gen"
import LiveHeader from "../components/LiveHeader.vue"
import SidePanel from "../components/SidePanel.vue"
import StateBlock from "../components/StateBlock.vue"
import { friendlyError } from "../lib/errors"
import { useLiveData } from "../lib/live"
import { isSecure, toast } from "../stores/ui"

const router = useRouter()
const state = ref<"loading" | "ready" | "error">("loading")
const errorText = ref("")
const steps = ref<SetupStep[]>([])
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

function mark(s: SetupStep, i: number): { text: string; cls: string } {
  switch (s.state) {
    case "done":
      return { text: "✓", cls: "is-done" }
    case "unknown":
      return { text: "?", cls: "is-unknown" }
    case "open":
      return { text: String(i + 1), cls: "is-on" }
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
  <div class="page">
    <div class="page-h">
      <h1 class="t-20">Getting started</h1>
      <LiveHeader :updated-at="updatedAt" :stale="stale" @refresh="refresh" />
      <button class="btn btn-secondary" type="button" @click="refresh">Check again</button>
    </div>
    <p class="c-2">
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
    <div v-else class="card list">
      <div v-for="(s, i) in steps" :key="s.name" class="li top">
        <span class="lead step" :class="mark(s, i).cls"
          ><span class="n">{{ mark(s, i).text }}</span></span
        >
        <span class="grow">
          <b>{{ s.name }}</b> <span class="c-2">{{ s.detail }}</span>
          <template v-if="s.state === 'open'">
            <br />
            <button v-if="s.name === 'HTTPS'" class="link" type="button" @click="helpOpen = true">
              How to turn on HTTPS
            </button>
          </template>
        </span>
        <button
          v-if="s.state === 'unknown'"
          class="btn btn-secondary"
          type="button"
          :disabled="busy"
          @click="runDoctor"
        >
          Run doctor
        </button>
        <template v-else-if="s.state === 'open' && s.command">
          <code>{{ s.command }}</code>
          <button
            v-if="isSecure()"
            class="btn btn-secondary"
            type="button"
            @click="copy(s.command)"
          >
            Copy
          </button>
          <p v-else class="t-12 c-3">
            Copy needs HTTPS or localhost.
            <button class="link" type="button" @click="helpOpen = true">
              How to turn on HTTPS
            </button>
          </p>
        </template>
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
