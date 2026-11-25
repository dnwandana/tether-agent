// Types the deep import of one Lucide icon. Each icon module exports its component and its SVG
// node. The page needs the node to draw an icon in an HTML string.
declare module "@lucide/vue/dist/esm/icons/*.mjs" {
  import type { LucideIcon } from "@lucide/vue"
  // Lucide names the export __iconData.
  // oxlint-disable-next-line no-underscore-dangle
  export const __iconData: {
    name: string
    size: number
    node: [tag: string, attrs: Record<string, string | number>][]
    aliases?: string[]
  }
  const icon: LucideIcon
  export default icon
}
