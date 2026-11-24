<script setup lang="ts">
import { ref } from "vue"
import { InfoIcon } from "@lucide/vue"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import SidePanel from "./SidePanel.vue"

const open = ref(false)
const host = location.hostname
</script>

<template>
  <Alert>
    <InfoIcon />
    <AlertDescription>
      <span>
        Voice notes, Copy, and notifications need HTTPS or localhost.
        <Button variant="link" size="sm" class="h-auto p-0" type="button" @click="open = true">
          How to turn on HTTPS
        </Button>
      </span>
    </AlertDescription>
  </Alert>
  <SidePanel :open="open" title="Turn on HTTPS" @close="open = false">
    <p>
      Voice notes, Copy, and browser notifications need HTTPS, or a page on localhost. Pick one way:
    </p>
    <ol class="list-decimal pl-5">
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
