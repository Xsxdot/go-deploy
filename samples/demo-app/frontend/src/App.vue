<template>
  <div class="app">
    <h1>Demo App</h1>
    <p v-if="loading">Checking API status...</p>
    <p v-else-if="error" class="error">{{ error }}</p>
    <p v-else class="status">API Status: {{ status }}</p>
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'

const loading = ref(true)
const status = ref('')
const error = ref('')

onMounted(async () => {
  try {
    const res = await fetch('/api/health')
    const data = await res.json()
    status.value = data.status || 'ok'
  } catch (e) {
    error.value = e.message || 'Failed to fetch'
  } finally {
    loading.value = false
  }
})
</script>

<style scoped>
.app {
  font-family: system-ui, sans-serif;
  max-width: 600px;
  margin: 2rem auto;
  padding: 1rem;
}
h1 {
  color: #333;
}
.status {
  color: #22c55e;
  font-weight: 500;
}
.error {
  color: #ef4444;
}
</style>
