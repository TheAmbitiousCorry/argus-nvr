<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import CameraPicker from '@/components/CameraPicker.vue'
import ErrorBanner from '@/components/ErrorBanner.vue'
import SettingField from '@/components/SettingField.vue'
import { ApiError, api } from '@/api/client'
import { useCameraConfigs } from '@/composables/useCameraConfigs'
import { isOnline, useCameraStore } from '@/composables/useCameraStore'
import {
  CONFIG_READINGS,
  FIELDS_BY_KEY,
  FIELD_GROUPS,
  LIVE_READINGS,
  type Distinct,
} from '@/settings/fields'
import type { BulkResult, SettingsPatch, SettingsRequest } from '@/types'

const { cameras, statuses, loaded } = useCameraStore()
const { configs, errors, loading, load } = useCameraConfigs()

const selected = ref<string[]>([])
/** Only the fields somebody actually touched. The patch is partial by design. */
const draft = ref<Record<string, string | number | boolean>>({})
const applying = ref(false)
const applyError = ref<string | null>(null)
const results = ref<BulkResult[] | null>(null)

/** Start on everything reachable, so the page opens with real values on it. */
let seeded = false
watch(
  () => cameras.value.length,
  () => {
    if (seeded || cameras.value.length === 0) return
    seeded = true
    const online = cameras.value.filter(isOnline).map((c) => c.id)
    selected.value = online.length > 0 ? online : cameras.value.map((c) => c.id)
  },
  { immediate: true },
)

watch(selected, (ids) => void load(ids), { immediate: true })

const selectedCameras = computed(() =>
  cameras.value.filter((c) => selected.value.includes(c.id)),
)
const withConfig = computed(() => selectedCameras.value.filter((c) => configs.value[c.id]))

/** What the picker shows under each name while this view is doing something. */
const pickerDetail = computed(() => {
  const out: Record<string, string> = {}
  for (const cam of cameras.value) {
    if (loading.value[cam.id]) out[cam.id] = 'reading config...'
    else if (errors.value[cam.id]) out[cam.id] = 'config unavailable'
  }
  return out
})

/**
 * Every value the selected cameras hold for a field, grouped. One entry means
 * they agree; more than one means they disagree, and the control has to say so
 * rather than pick one and quietly overwrite the others on save.
 */
const distinctByKey = computed(() => {
  const out: Record<string, Distinct[]> = {}
  for (const spec of FIELDS_BY_KEY.values()) {
    const groups: Distinct[] = []
    for (const cam of withConfig.value) {
      const value = configs.value[cam.id]?.[spec.key]
      if (value === undefined) continue
      const hit = groups.find((g) => Object.is(g.value, value))
      if (hit) hit.cameras.push(cam.name)
      else groups.push({ value, cameras: [cam.name] })
    }
    out[spec.key] = groups
  }
  return out
})

function effective(key: string): unknown {
  if (key in draft.value) return draft.value[key]
  const groups = distinctByKey.value[key] ?? []
  return groups.length === 1 ? groups[0].value : undefined
}

/** The selection's settled values, for the fields that own other fields. */
const effectiveValues = computed(() => {
  const out: Record<string, unknown> = {}
  for (const key of FIELDS_BY_KEY.keys()) out[key] = effective(key)
  return out
})

const changedKeys = computed(() => Object.keys(draft.value))

const anyLoading = computed(() => selected.value.some((id) => loading.value[id]))
const noneReadable = computed(
  () => selected.value.length > 0 && withConfig.value.length === 0 && !anyLoading.value,
)

function setField(key: string, value: string | number | boolean) {
  draft.value = { ...draft.value, [key]: value }
}

function resetField(key: string) {
  const next = { ...draft.value }
  delete next[key]
  draft.value = next
}

function discard() {
  draft.value = {}
  results.value = null
}

async function reload() {
  results.value = null
  await load(selected.value, true)
}

async function apply() {
  if (applying.value || changedKeys.value.length === 0 || selected.value.length === 0) return
  applying.value = true
  applyError.value = null
  results.value = null

  const image: SettingsPatch = {}
  const recording: SettingsPatch = {}
  for (const [key, value] of Object.entries(draft.value)) {
    const spec = FIELDS_BY_KEY.get(key)
    if (!spec) continue
    ;(spec.form === 'image' ? image : recording)[key] = value
  }

  const body: SettingsRequest = { cameraIds: [...selected.value] }
  if (Object.keys(image).length > 0) body.image = image
  if (Object.keys(recording).length > 0) body.recording = recording

  try {
    const res = await api.applySettings(body)
    results.value = res?.results ?? []
    // Read the cameras back rather than assuming the patch landed: a camera can
    // accept the form and still refuse a control the sensor does not have.
    draft.value = {}
    await load(selected.value, true)
  } catch (err) {
    applyError.value = err instanceof ApiError ? err.message : String(err)
  } finally {
    applying.value = false
  }
}

const resultName = (id: string) => cameras.value.find((c) => c.id === id)?.name ?? id

function readingText(cameraId: string, key: string, source: 'config' | 'record'): string {
  const raw =
    source === 'config'
      ? configs.value[cameraId]?.[key]
      : (statuses.value[cameraId] as unknown as Record<string, unknown> | undefined)?.[key]
  if (raw === undefined || raw === null) return '--'
  if (typeof raw === 'boolean') return raw ? 'yes' : 'no'
  if (typeof raw === 'number') return Number.isInteger(raw) ? String(raw) : raw.toFixed(1)
  if (raw === '') return 'none'
  return String(raw)
}
</script>

<template>
  <section class="settings">
    <header class="head">
      <h1>Settings</h1>
      <p class="note">
        Every setting the firmware stores, read straight from each camera's /config. Changes
        go to the selected cameras together as one partial update.
      </p>
    </header>

    <div class="card">
      <h2>Cameras</h2>
      <CameraPicker
        v-model="selected"
        :cameras="cameras"
        :statuses="statuses"
        :detail="pickerDetail"
      />
      <p v-if="!loaded" class="note">Loading cameras...</p>
      <p v-else-if="selected.length === 0" class="note">
        Pick at least one camera to see and change its settings.
      </p>
      <p v-else-if="anyLoading" class="note">Reading configuration...</p>
      <p v-else class="note">
        Showing {{ withConfig.length }} of {{ selected.length }} selected cameras. A field the
        selection disagrees on is marked <span class="inline-chip">varies</span> and is left
        alone unless you change it.
      </p>

      <ErrorBanner
        v-if="noneReadable"
        message="None of the selected cameras answered /config."
        action-label="Retry"
        @action="reload"
      />
    </div>

    <template v-if="withConfig.length > 0">
      <div v-for="group in FIELD_GROUPS" :key="group.id" class="card">
        <h2>{{ group.title }}</h2>
        <p v-if="group.note" class="note">{{ group.note }}</p>
        <div class="fields">
          <SettingField
            v-for="spec in group.fields"
            :key="spec.key"
            :spec="spec"
            :distinct="distinctByKey[spec.key] ?? []"
            :value="effective(spec.key)"
            :touched="spec.key in draft"
            :disabled="spec.overriddenBy?.(effectiveValues) ?? false"
            disabled-reason="Auto image is on, so the camera owns this and ignores what is sent."
            @change="(v) => setField(spec.key, v)"
            @reset="resetField(spec.key)"
          />
        </div>
      </div>

      <div class="card">
        <h2>Reported by the camera</h2>
        <p class="note">
          Read-only. The stored settings above are what you asked for; these are what the
          camera and its sensor actually report, and no form accepts them.
        </p>

        <div class="readings">
          <div v-for="cam in withConfig" :key="cam.id" class="reading-card">
            <h3>{{ cam.name }}</h3>
            <dl>
              <template v-for="r in CONFIG_READINGS" :key="r.key">
                <dt :title="r.hint">{{ r.label }}</dt>
                <dd>{{ readingText(cam.id, r.key, 'config') }}</dd>
              </template>
              <template v-for="r in LIVE_READINGS" :key="r.key">
                <dt class="live" :title="r.hint">{{ r.label }}</dt>
                <dd class="live">
                  {{ readingText(cam.id, r.key, 'record') }}{{ r.unit ? ` ${r.unit}` : '' }}
                </dd>
              </template>
            </dl>
            <p class="note small">Live rows update with the two second /record poll.</p>
          </div>
        </div>
      </div>
    </template>

    <div v-if="results" class="card">
      <h2>Result</h2>
      <ul class="results">
        <li v-for="r in results" :key="r.cameraId" :class="r.ok ? 'ok' : 'bad'">
          <span class="res-name">{{ resultName(r.cameraId) }}</span>
          <span class="res-text">{{ r.ok ? 'applied' : (r.error ?? 'failed') }}</span>
        </li>
      </ul>
      <p class="note">Values above were read back from the cameras after applying.</p>
    </div>

    <div class="actions">
      <ErrorBanner v-if="applyError" :message="applyError" />
      <div class="action-row">
        <span class="pending">
          <template v-if="changedKeys.length === 0">No changes</template>
          <template v-else>
            {{ changedKeys.length }}
            {{ changedKeys.length === 1 ? 'field' : 'fields' }} on
            {{ selected.length }} {{ selected.length === 1 ? 'camera' : 'cameras' }}:
            <code>{{ changedKeys.join(', ') }}</code>
          </template>
        </span>
        <button type="button" class="ghost" :disabled="applying" @click="reload">
          Reload from cameras
        </button>
        <button
          type="button"
          class="ghost"
          :disabled="applying || changedKeys.length === 0"
          @click="discard"
        >
          Discard
        </button>
        <button
          type="button"
          class="primary"
          :disabled="applying || changedKeys.length === 0 || selected.length === 0"
          @click="apply"
        >
          {{ applying ? 'Applying...' : 'Apply to selected' }}
        </button>
      </div>
    </div>
  </section>
</template>

<style scoped>
.settings {
  display: flex;
  flex-direction: column;
  gap: 0.9rem;
  padding-bottom: 4.5rem;
}

.head {
  display: flex;
  flex-direction: column;
  gap: 0.2rem;
}

h1 {
  margin: 0;
  font-size: 1.15rem;
  font-weight: 600;
}

h2 {
  margin: 0;
  font-size: 0.95rem;
  font-weight: 600;
}

h3 {
  margin: 0 0 0.3rem;
  font-size: 0.82rem;
  font-weight: 600;
  color: #ddd;
}

.card {
  display: flex;
  flex-direction: column;
  gap: 0.6rem;
  padding: 1rem;
  background: #181818;
  border: 1px solid #242424;
  border-radius: 10px;
}

.note {
  margin: 0;
  font-size: 0.76rem;
  color: #8a8a8a;
}
.note.small {
  font-size: 0.7rem;
}

.inline-chip {
  font-size: 0.65rem;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  padding: 0.05rem 0.3rem;
  border-radius: 4px;
  color: #1a1405;
  background: #c90;
}

.fields {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 0.5rem;
}

.readings {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(260px, 1fr));
  gap: 0.5rem;
}

.reading-card {
  padding: 0.6rem 0.7rem;
  background: #111;
  border: 1px solid #242424;
  border-radius: 8px;
}

dl {
  display: grid;
  grid-template-columns: auto 1fr;
  gap: 0.2rem 0.75rem;
  margin: 0;
  font-size: 0.76rem;
}

dt {
  color: #7a7a7a;
}

dd {
  margin: 0;
  color: #ccc;
  font-variant-numeric: tabular-nums;
  overflow-wrap: anywhere;
}

dt.live,
dd.live {
  color: #6f8f80;
}
dd.live {
  color: #9ccbb8;
}

.results {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 0.3rem;
  font-size: 0.8rem;
}

.results li {
  display: flex;
  gap: 0.6rem;
  padding: 0.35rem 0.6rem;
  background: #111;
  border: 1px solid #242424;
  border-left: 2px solid #555;
  border-radius: 6px;
}
.results li.ok {
  border-left-color: #2a7;
}
.results li.bad {
  border-left-color: #f55;
}

.res-name {
  color: #ddd;
  min-width: 8rem;
}
.results li.ok .res-text {
  color: #2a7;
}
.results li.bad .res-text {
  color: #f55;
}

.actions {
  position: sticky;
  bottom: 0;
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
  padding: 0.7rem;
  background: #161616;
  border: 1px solid #242424;
  border-radius: 10px;
}

.action-row {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  flex-wrap: wrap;
}

.pending {
  flex: 1 1 12rem;
  font-size: 0.78rem;
  color: #9a9a9a;
}

.pending code {
  font-size: 0.72rem;
  color: #2a7;
  overflow-wrap: anywhere;
}

.primary {
  padding: 0.5rem 0.95rem;
  font: inherit;
  font-size: 0.85rem;
  font-weight: 600;
  color: #06120d;
  background: #2a7;
  border: none;
  border-radius: 6px;
  cursor: pointer;
}
.primary:disabled {
  opacity: 0.45;
  cursor: default;
}

.ghost {
  padding: 0.45rem 0.85rem;
  font: inherit;
  font-size: 0.82rem;
  color: #ccc;
  background: transparent;
  border: 1px solid #2c2c2c;
  border-radius: 6px;
  cursor: pointer;
}
.ghost:hover:not(:disabled) {
  border-color: #2a7;
  color: #2a7;
}
.ghost:disabled {
  opacity: 0.45;
  cursor: default;
}
</style>
