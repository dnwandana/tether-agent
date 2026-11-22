<script setup lang="ts">
import { onMounted, ref } from "vue"
import { api } from "../api/client"
import type { SetupStep } from "../api/types.gen"
import SidePanel from "../components/SidePanel.vue"
import StateBlock from "../components/StateBlock.vue"
import { isSecure, toast } from "../stores/ui"

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
    errorText.value = e instanceof Error ? e.message : String(e)
    state.value = "error"
  }
}

async function copy(text: string): Promise<void> {
  try {
    await navigator.clipboard.writeText(text)
    toast("Copied.")
  } catch (e) {
    toast(e instanceof Error ? e.message : String(e), "error")
  }
}

onMounted(load)
</script>

<template>
  <div class="page">
    <div class="page-h"><h1 class="t-20">Getting started</h1></div>
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
      @retry="load"
    />
    <StateBlock v-else-if="steps.length === 0" state="empty" text="No setup steps" />
    <div v-else class="card list">
      <div v-for="(s, i) in steps" :key="s.name" class="li top">
        <span class="lead step" :class="s.state === 'ok' ? 'is-done' : 'is-on'"
          ><span class="n">{{ s.state === "ok" ? "✓" : i + 1 }}</span></span
        >
        <span class="grow">
          <b>{{ s.name }}</b> <span class="c-2">{{ s.detail }}</span>
          <template v-if="s.state !== 'ok'">
            <br />
            <button v-if="s.name === 'HTTPS'" class="link" type="button" @click="helpOpen = true">
              How to turn on HTTPS
            </button>
          </template>
        </span>
        <template v-if="s.state !== 'ok' && s.command">
          <code>{{ s.command }}</code>
          <button
            v-if="isSecure()"
            class="btn btn-secondary"
            type="button"
            @click="copy(s.command)"
          >
            Copy
          </button>
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
