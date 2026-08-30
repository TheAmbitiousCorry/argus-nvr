<script setup lang="ts">
import { computed } from 'vue'
import { DAY_LABELS, type Distinct, type FieldSpec } from '@/settings/fields'

const props = defineProps<{
  spec: FieldSpec
  distinct: Distinct[]
  /** What the control should show: the edit if there is one, otherwise the shared value. */
  value: unknown
  touched: boolean
  disabled?: boolean
  disabledReason?: string
}>()

const emit = defineEmits<{ change: [string | number | boolean]; reset: [] }>()

/**
 * The whole point of this component: cameras that disagree say so rather than
 * showing the first one's value, because showing it would overwrite the rest on
 * save without anyone asking for that.
 */
const mixed = computed(() => !props.touched && props.distinct.length > 1)
const unreported = computed(() => !props.touched && props.distinct.length === 0)

const numeric = computed(() => (typeof props.value === 'number' ? props.value : undefined))
const checked = computed(() => props.value === true)
const selectValue = computed(() =>
  mixed.value || numeric.value === undefined ? '' : String(numeric.value),
)
const dayBits = computed(() => numeric.value ?? 0)

/** Only the ends the contract actually gives. A lone minimum is not a range. */
const rangeText = computed(() => {
  const { min, max } = props.spec
  if (min !== undefined && max !== undefined) return `${min} to ${max}`
  if (min !== undefined) return `${min} or more`
  if (max !== undefined) return `up to ${max}`
  return ''
})

function describe(value: unknown): string {
  if (props.spec.kind === 'bool') return value === true ? 'on' : 'off'
  if (props.spec.kind === 'days') {
    const bits = typeof value === 'number' ? value : 0
    const days = DAY_LABELS.filter((_, i) => (bits & (1 << i)) !== 0)
    return days.length === 0 ? 'no days' : days.join(' ')
  }
  if (props.spec.kind === 'enum' && typeof value === 'number') {
    const hit = props.spec.options?.find((o) => o.value === value)
    return hit ? hit.label : String(value)
  }
  return value === undefined || value === null || value === '' ? 'not set' : String(value)
}

const variesText = computed(() =>
  props.distinct.map((d) => `${describe(d.value)} on ${d.cameras.join(', ')}`).join('; '),
)

/** An enum value the sensor reports that is not in the list still has to be selectable. */
const strayOption = computed(() => {
  if (props.spec.kind !== 'enum' || numeric.value === undefined || mixed.value) return null
  const known = props.spec.options?.some((o) => o.value === numeric.value)
  return known ? null : numeric.value
})

function onBool(ev: Event) {
  emit('change', (ev.target as HTMLInputElement).checked)
}

function onNumber(ev: Event) {
  const raw = (ev.target as HTMLInputElement).value
  if (raw.trim() === '') {
    emit('reset')
    return
  }
  const n = Number(raw)
  if (Number.isFinite(n)) emit('change', n)
}

function onSelect(ev: Event) {
  const raw = (ev.target as HTMLSelectElement).value
  if (raw === '') {
    emit('reset')
    return
  }
  emit('change', Number(raw))
}

function toggleDay(index: number) {
  emit('change', dayBits.value ^ (1 << index))
}
</script>

<template>
  <div class="field" :class="{ touched, mixed, off: disabled }">
    <div class="head">
      <span class="label">{{ spec.label }}</span>
      <code class="key">{{ spec.key }}</code>
      <span v-if="touched" class="chip will">will change</span>
      <span v-else-if="mixed" class="chip varies">varies</span>
      <button v-if="touched" type="button" class="link" @click="emit('reset')">undo</button>
    </div>

    <div class="control">
      <label v-if="spec.kind === 'bool'" class="switch">
        <input
          type="checkbox"
          :checked="checked"
          :indeterminate="mixed"
          :disabled="disabled"
          @change="onBool"
        />
        <span>{{ mixed ? 'varies across the selection' : checked ? 'on' : 'off' }}</span>
      </label>

      <template v-else-if="spec.kind === 'int'">
        <input
          type="number"
          :value="mixed || numeric === undefined ? '' : numeric"
          :min="spec.min"
          :max="spec.max"
          :placeholder="mixed ? 'varies' : unreported ? 'not reported' : ''"
          :disabled="disabled"
          @input="onNumber"
        />
        <span v-if="spec.unit" class="unit">{{ spec.unit }}</span>
        <span v-if="rangeText" class="range">{{ rangeText }}</span>
      </template>

      <select
        v-else-if="spec.kind === 'enum'"
        :value="selectValue"
        :disabled="disabled"
        @change="onSelect"
      >
        <option v-if="mixed || numeric === undefined" value="">
          {{ mixed ? 'varies - pick to set all' : 'not reported' }}
        </option>
        <option v-if="strayOption !== null" :value="String(strayOption)">
          {{ strayOption }} - reported by the camera
        </option>
        <option v-for="opt in spec.options" :key="opt.value" :value="String(opt.value)">
          {{ opt.label }}
        </option>
      </select>

      <template v-else-if="spec.kind === 'days'">
        <div class="days">
          <button
            v-for="(day, i) in DAY_LABELS"
            :key="day"
            type="button"
            class="day"
            :class="{ on: !mixed && (dayBits & (1 << i)) !== 0 }"
            :disabled="disabled || mixed"
            :aria-pressed="!mixed && (dayBits & (1 << i)) !== 0"
            @click="toggleDay(i)"
          >
            {{ day }}
          </button>
        </div>
        <button
          v-if="mixed"
          type="button"
          class="link"
          :disabled="disabled"
          @click="emit('change', 0)"
        >
          Replace on every selected camera
        </button>
        <span v-else class="unit">bitmask {{ dayBits }}</span>
      </template>
    </div>

    <p v-if="mixed" class="varies-detail">{{ variesText }}</p>
    <p v-else-if="unreported" class="hint">No selected camera reported this field.</p>
    <p v-if="disabled && disabledReason" class="hint">{{ disabledReason }}</p>
    <p v-else-if="spec.hint" class="hint">{{ spec.hint }}</p>
  </div>
</template>

<style scoped>
.field {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
  padding: 0.55rem 0.65rem;
  background: #111;
  border: 1px solid #242424;
  border-left: 2px solid #242424;
  border-radius: 8px;
}
.field.touched {
  border-left-color: #2a7;
  background: #101613;
}
.field.mixed {
  border-left-color: #c90;
}
.field.off {
  opacity: 0.6;
}

.head {
  display: flex;
  align-items: center;
  gap: 0.4rem;
  flex-wrap: wrap;
}

.label {
  font-size: 0.82rem;
  color: #ddd;
}

.key {
  font-size: 0.68rem;
  color: #6a6a6a;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
}

.chip {
  font-size: 0.65rem;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  padding: 0.05rem 0.3rem;
  border-radius: 4px;
}
.chip.will {
  color: #06120d;
  background: #2a7;
}
.chip.varies {
  color: #1a1405;
  background: #c90;
}

.control {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  flex-wrap: wrap;
}

input[type='number'] {
  width: 7rem;
  padding: 0.35rem 0.5rem;
  font: inherit;
  font-size: 0.85rem;
  font-variant-numeric: tabular-nums;
  color: #eee;
  background: #0d0d0d;
  border: 1px solid #2c2c2c;
  border-radius: 6px;
}

select {
  padding: 0.35rem 0.5rem;
  font: inherit;
  font-size: 0.85rem;
  color: #eee;
  background: #0d0d0d;
  border: 1px solid #2c2c2c;
  border-radius: 6px;
  max-width: 100%;
}

input:focus,
select:focus {
  outline: none;
  border-color: #2a7;
}

input:disabled,
select:disabled {
  cursor: not-allowed;
}

.switch {
  display: inline-flex;
  align-items: center;
  gap: 0.45rem;
  font-size: 0.82rem;
  color: #bbb;
  cursor: pointer;
}
.switch input {
  accent-color: #2a7;
  width: 1rem;
  height: 1rem;
}

.days {
  display: flex;
  gap: 0.25rem;
  flex-wrap: wrap;
}

.day {
  padding: 0.25rem 0.45rem;
  font: inherit;
  font-size: 0.72rem;
  color: #9a9a9a;
  background: #0d0d0d;
  border: 1px solid #2c2c2c;
  border-radius: 5px;
  cursor: pointer;
}
.day.on {
  color: #06120d;
  background: #2a7;
  border-color: #2a7;
}
.day:disabled {
  cursor: not-allowed;
}

.unit,
.range {
  font-size: 0.72rem;
  color: #7a7a7a;
}

.link {
  padding: 0;
  font: inherit;
  font-size: 0.72rem;
  color: #2a7;
  background: none;
  border: none;
  cursor: pointer;
}
.link:hover {
  text-decoration: underline;
}

.hint,
.varies-detail {
  margin: 0;
  font-size: 0.71rem;
  color: #7a7a7a;
}
.varies-detail {
  color: #c90;
}
</style>
