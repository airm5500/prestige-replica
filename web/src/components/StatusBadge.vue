<script setup lang="ts">
import type { Health, Severity } from '../api'
import { healthLabel, severityLabel } from '../format'

const props = defineProps<{ health?: Health; severity?: Severity | null }>()
const cls = () => (props.health ?? props.severity ?? 'UNKNOWN').toLowerCase()
const text = () => (props.health ? healthLabel[props.health] : props.severity ? severityLabel[props.severity] : '—')
</script>

<template>
  <span class="badge" :class="cls()" role="status">{{ text() }}</span>
</template>
