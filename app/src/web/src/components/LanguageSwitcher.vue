<script setup lang="ts">
import { useI18n } from "vue-i18n";
import { getLocale, setLocale, type SupportedLocale } from "@/i18n";

const { t } = useI18n();

function toggle(): void {
  const next: SupportedLocale = getLocale() === "zh" ? "en" : "zh";
  setLocale(next);
}
</script>

<template>
  <el-button link class="lang-switcher" @click="toggle">
    {{ getLocale() === "zh" ? t("common.english") : t("common.chinese") }}
  </el-button>
</template>

<style scoped>
.lang-switcher {
  font-size: 14px;
}
</style>
