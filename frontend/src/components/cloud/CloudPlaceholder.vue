<template>
  <div class="main-content-container cloud-page">
    <el-card shadow="never" class="cloud-card">
      <template #header>
        <span>{{ sourceName }} · {{ title }}</span>
      </template>
      <el-empty :description="description" />
      <template v-if="related.length">
        <div class="related-title">相关功能：</div>
        <div class="related-links">
          <el-button
            v-for="(r, i) in related"
            :key="i"
            size="small"
            @click="$router.push(r.path)"
          >
            {{ r.name }}
          </el-button>
        </div>
      </template>
    </el-card>
  </div>
</template>

<script setup lang="ts">
defineProps<{
  sourceName: string
  title: string
  description: string
  related: { name: string; path: string }[]
}>()
</script>

<style scoped>
.cloud-page {
  padding: 16px;
}
.cloud-card {
  max-width: 1100px;
}
.related-title {
  font-size: 13px;
  color: #909399;
  margin: 8px 0 8px;
}
.related-links {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}
</style>