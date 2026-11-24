<script setup lang="ts">
import { BRAND_PATH } from "../lib/icons"
</script>

<template>
  <span
    data-slot="brand-mark"
    class="inline-flex size-6 shrink-0 items-center justify-center rounded-md bg-foreground text-background"
    aria-hidden="true"
  >
    <!-- The path holds the padding of the tile, so the SVG fills the whole 24 px tile. -->
    <svg viewBox="0 0 24 24" width="24" height="24">
      <path
        :d="BRAND_PATH"
        fill="none"
        stroke="currentColor"
        stroke-width="3.1"
        stroke-linecap="round"
        stroke-linejoin="round"
      />
      <circle cx="17.28" cy="15.84" r="1.56" fill="currentColor" stroke="none" />
    </svg>
  </span>
</template>
