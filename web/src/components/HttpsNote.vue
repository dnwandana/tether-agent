<script setup lang="ts">
import { ref } from "vue"
import SidePanel from "./SidePanel.vue"

const open = ref(false)
const host = location.hostname
</script>

<template>
  <p class="https-line t-12 c-3">
    Voice notes, Copy, and notifications need HTTPS or localhost.
    <button class="link" type="button" @click="open = true">How to turn on HTTPS</button>
  </p>
  <SidePanel :open="open" title="Turn on HTTPS" @close="open = false">
    <p>
      Voice notes, Copy, and browser notifications need HTTPS, or a page on localhost. Pick one way:
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
</template>

<style scoped>
.https-line {
  margin: 0;
  font-size: 12px;
  overflow-wrap: anywhere;
}
</style>
