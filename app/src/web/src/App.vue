<script setup lang="ts">
import { RouterView } from "vue-router";
import { useShellBridge } from "@/composables/useShellBridge";
// iframe 嵌入时同步壳的登录态（跨域 localStorage 不共享）。
useShellBridge();
</script>

<template>
  <router-view />
</template>
