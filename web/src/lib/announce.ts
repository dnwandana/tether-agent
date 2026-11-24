// Puts a text in the live region of App.vue. The region is outside the cards, because a region
// inside a card would read each countdown tick. The clear first makes a reader read a repeated text.
let timer: ReturnType<typeof setTimeout> | null = null

export function announce(text: string): void {
  const region = document.querySelector<HTMLElement>(".announce")
  if (!region) return
  region.textContent = ""
  // A newer call replaces the older text that still waits.
  if (timer) clearTimeout(timer)
  timer = setTimeout(() => {
    timer = null
    region.textContent = text
  }, 50)
}
