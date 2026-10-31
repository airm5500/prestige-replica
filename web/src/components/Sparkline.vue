<script setup lang="ts">
import { computed } from 'vue'

/** Courbe simple sans dépendance. Les valeurs nulles créent une coupure. */
const props = defineProps<{ values: (number | null)[]; label: string; unit?: string; height?: number }>()
const h = computed(() => props.height ?? 60)
const w = 600
const max = computed(() => Math.max(1, ...props.values.filter((v): v is number => v !== null)))
const path = computed(() => {
  const n = props.values.length
  if (n < 2) return ''
  let d = ''
  let pen = false
  props.values.forEach((v, i) => {
    if (v === null) { pen = false; return }
    const x = (i / (n - 1)) * w
    const y = h.value - (v / max.value) * (h.value - 4) - 2
    d += `${pen ? 'L' : 'M'}${x.toFixed(1)},${y.toFixed(1)} `
    pen = true
  })
  return d
})
const last = computed(() => [...props.values].reverse().find((v) => v !== null) ?? null)
</script>

<template>
  <figure class="spark">
    <figcaption>{{ label }} — dernier : <strong>{{ last ?? '—' }}{{ last !== null && unit ? ' ' + unit : '' }}</strong>, max : {{ max }}{{ unit ? ' ' + unit : '' }}</figcaption>
    <svg :viewBox="`0 0 ${w} ${h}`" preserveAspectRatio="none" :height="h" role="img" :aria-label="label">
      <path :d="path" fill="none" stroke="currentColor" stroke-width="2" vector-effect="non-scaling-stroke" />
    </svg>
  </figure>
</template>
