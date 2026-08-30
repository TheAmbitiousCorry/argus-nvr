<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import ErrorBanner from '@/components/ErrorBanner.vue'
import { ApiError, api } from '@/api/client'
import { useCameraStore } from '@/composables/useCameraStore'
import type { DiscoveredCamera } from '@/types'

const DISCOVERY_POLL_MS = 10000

const router = useRouter()
const { cameras, addCamera } = useCameraStore()

const form = reactive({ address: '', name: '', user: '', pass: '' })
const submitting = ref(false)
const formError = ref<string | null>(null)
const success = ref<string | null>(null)

const discovered = ref<DiscoveredCamera[]>([])
const discoveryError = ref<string | null>(null)
const discoveryLoaded = ref(false)
const addingAddress = ref<string | null>(null)
let discoveryTimer: ReturnType<typeof setInterval> | null = null

/** The backend already filters these, but a stale poll can still overlap. */
const knownAddresses = computed(
  () => new Set(cameras.value.map((c) => c.address.trim().toLowerCase())),
)
const unaddedDiscoveries = computed(() =>
  discovered.value.filter((d) => !knownAddresses.value.has(d.address.trim().toLowerCase())),
)

async function loadDiscovered() {
  try {
    const list = await api.listDiscovered()
    discovered.value = Array.isArray(list) ? list : []
    discoveryError.value = null
  } catch (err) {
    discoveryError.value = err instanceof ApiError ? err.message : String(err)
  } finally {
    discoveryLoaded.value = true
  }
}

function describe(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.status === 401 || err.status === 403) {
      return 'The camera rejected those credentials.'
    }
    return err.message
  }
  return String(err)
}

async function submit() {
  if (submitting.value) return
  formError.value = null
  success.value = null
  const address = form.address.trim()
  if (!address) {
    formError.value = 'An address is required.'
    return
  }
  submitting.value = true
  try {
    await addCamera({
      address,
      name: form.name.trim() || address,
      user: form.user,
      pass: form.pass,
    })
    success.value = `Added ${form.name.trim() || address}.`
    form.address = ''
    form.name = ''
    await loadDiscovered()
  } catch (err) {
    formError.value = describe(err)
  } finally {
    submitting.value = false
  }
}

/**
 * One click adds a discovered camera with the credentials typed into the form
 * above, since a fleet of these normally shares one login.
 */
async function addDiscovered(entry: DiscoveredCamera) {
  if (addingAddress.value) return
  addingAddress.value = entry.address
  formError.value = null
  success.value = null
  try {
    await addCamera({
      address: entry.address,
      name: entry.name || entry.address,
      user: form.user,
      pass: form.pass,
    })
    success.value = `Added ${entry.name || entry.address}.`
    await loadDiscovered()
  } catch (err) {
    formError.value = describe(err)
  } finally {
    addingAddress.value = null
  }
}

onMounted(() => {
  void loadDiscovered()
  discoveryTimer = setInterval(() => void loadDiscovered(), DISCOVERY_POLL_MS)
})
onBeforeUnmount(() => {
  if (discoveryTimer) clearInterval(discoveryTimer)
})
</script>

<template>
  <section class="add">
    <header class="head">
      <h1>Add a camera</h1>
      <button type="button" class="ghost" @click="router.push('/')">Back to wall</button>
    </header>

    <div class="columns">
      <form class="card form" @submit.prevent="submit">
        <h2>By address</h2>

        <label>
          <span>Address</span>
          <input
            v-model="form.address"
            type="text"
            required
            autocomplete="off"
            spellcheck="false"
            placeholder="192.168.10.208 or camera-alpha.local"
          />
        </label>

        <label>
          <span>Name</span>
          <input
            v-model="form.name"
            type="text"
            autocomplete="off"
            placeholder="Front door (defaults to the address)"
          />
        </label>

        <label>
          <span>Username</span>
          <input v-model="form.user" type="text" autocomplete="username" />
        </label>

        <label>
          <span>Password</span>
          <input v-model="form.pass" type="password" autocomplete="current-password" />
        </label>

        <p class="note">
          Credentials are sent to the NVR backend, which holds the camera session. They are
          also used by the one click adds on the right.
        </p>

        <ErrorBanner v-if="formError" :message="formError" />
        <p v-if="success" class="success" role="status">{{ success }}</p>

        <button type="submit" class="primary" :disabled="submitting">
          {{ submitting ? 'Adding...' : 'Add camera' }}
        </button>
      </form>

      <div class="card discovered">
        <h2>Found on the network</h2>
        <p class="note">Cameras advertising over mDNS that are not on the wall yet.</p>

        <ErrorBanner
          v-if="discoveryError"
          :message="`Discovery unavailable: ${discoveryError}`"
          action-label="Retry"
          @action="loadDiscovered"
        />

        <p v-else-if="!discoveryLoaded" class="note">Looking...</p>
        <p v-else-if="unaddedDiscoveries.length === 0" class="note">Nothing new found.</p>

        <ul v-else class="list">
          <li v-for="entry in unaddedDiscoveries" :key="entry.address">
            <div class="entry">
              <span class="entry-name">{{ entry.name || entry.address }}</span>
              <span class="entry-addr">{{ entry.address }}</span>
            </div>
            <button
              type="button"
              class="primary small"
              :disabled="addingAddress !== null"
              @click="addDiscovered(entry)"
            >
              {{ addingAddress === entry.address ? 'Adding...' : 'Add' }}
            </button>
          </li>
        </ul>
      </div>
    </div>
  </section>
</template>

<style scoped>
.add {
  display: flex;
  flex-direction: column;
  gap: 1rem;
}

.head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.75rem;
}

h1 {
  margin: 0;
  font-size: 1.15rem;
  font-weight: 600;
}

h2 {
  margin: 0 0 0.25rem;
  font-size: 0.95rem;
  font-weight: 600;
}

.columns {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(320px, 1fr));
  gap: 0.9rem;
  align-items: start;
}

.card {
  display: flex;
  flex-direction: column;
  gap: 0.7rem;
  padding: 1rem;
  background: #181818;
  border: 1px solid #242424;
  border-radius: 10px;
}

label {
  display: flex;
  flex-direction: column;
  gap: 0.3rem;
  font-size: 0.8rem;
  color: #bbb;
}

input {
  padding: 0.55rem 0.65rem;
  font: inherit;
  font-size: 0.9rem;
  color: #eee;
  background: #111;
  border: 1px solid #2c2c2c;
  border-radius: 6px;
}
input:focus {
  outline: none;
  border-color: #2a7;
}

.note {
  margin: 0;
  font-size: 0.75rem;
  color: #8a8a8a;
}

.success {
  margin: 0;
  font-size: 0.82rem;
  color: #2a7;
}

.primary {
  align-self: flex-start;
  padding: 0.55rem 1rem;
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
  opacity: 0.55;
  cursor: default;
}
.primary.small {
  padding: 0.35rem 0.7rem;
  font-size: 0.78rem;
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
.ghost:hover {
  border-color: #2a7;
  color: #2a7;
}

.list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
}

.list li {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.75rem;
  padding: 0.55rem 0.7rem;
  background: #111;
  border: 1px solid #242424;
  border-radius: 8px;
}

.entry {
  display: flex;
  flex-direction: column;
  min-width: 0;
}

.entry-name {
  font-size: 0.85rem;
  font-weight: 600;
  color: #eee;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.entry-addr {
  font-size: 0.73rem;
  color: #8a8a8a;
}

@media (max-width: 700px) {
  .columns {
    grid-template-columns: 1fr;
  }
  .primary {
    align-self: stretch;
    text-align: center;
  }
  .primary.small {
    align-self: center;
  }
}
</style>
