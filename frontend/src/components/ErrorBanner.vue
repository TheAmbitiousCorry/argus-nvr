<script setup lang="ts">
defineProps<{ message: string; actionLabel?: string }>()
const emit = defineEmits<{ action: [] }>()
</script>

<template>
  <div class="banner" role="alert">
    <span class="mark" aria-hidden="true">!</span>
    <p class="text">{{ message }}</p>
    <button v-if="actionLabel" type="button" class="action" @click="emit('action')">
      {{ actionLabel }}
    </button>
  </div>
</template>

<style scoped>
.banner {
  display: flex;
  align-items: center;
  gap: 0.65rem;
  padding: 0.7rem 0.9rem;
  background: #241414;
  border: 1px solid #5a2020;
  border-radius: 8px;
  color: #f9c9c9;
}

.mark {
  flex: none;
  display: grid;
  place-items: center;
  width: 1.35rem;
  height: 1.35rem;
  font-weight: 700;
  font-size: 0.85rem;
  color: #111;
  background: #f55;
  border-radius: 50%;
}

.text {
  margin: 0;
  flex: 1 1 auto;
  font-size: 0.85rem;
  overflow-wrap: anywhere;
}

.action {
  flex: none;
  padding: 0.35rem 0.75rem;
  font: inherit;
  font-size: 0.78rem;
  color: #eee;
  background: transparent;
  border: 1px solid #5a2020;
  border-radius: 6px;
  cursor: pointer;
}
.action:hover {
  border-color: #f55;
  color: #f55;
}
</style>
