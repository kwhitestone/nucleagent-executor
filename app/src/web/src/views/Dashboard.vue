<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import { ElMessage } from "element-plus";
import { getHealth, getSessionHealth } from "@/api/executor";
import type { SessionHealth } from "@/api/types";
import { getDeviceInfo } from "@/config/device";
import { mockSessions } from "@/mock/sessions";
import LanguageSwitcher from "@/components/LanguageSwitcher.vue";
import HealthCard from "@/components/HealthCard.vue";
import DeviceInfoCard from "@/components/DeviceInfoCard.vue";
import SessionTable from "@/components/SessionTable.vue";

const { t } = useI18n();

const health = ref<string | null>(null);
const sessionHealth = ref<SessionHealth | null>(null);
const loading = ref(false);

const deviceInfo = computed(() => getDeviceInfo());
const sessions = computed(() => mockSessions);

async function loadHealth(): Promise<void> {
  loading.value = true;
  try {
    const [healthStr, sess] = await Promise.all([
      getHealth().catch((err: unknown) => {
        const msg = err instanceof Error ? err.message : t("executor.loadHealthFailed");
        ElMessage.error(msg);
        return null;
      }),
      getSessionHealth().catch((err: unknown) => {
        const msg = err instanceof Error ? err.message : t("executor.loadSessionHealthFailed");
        ElMessage.error(msg);
        return null;
      }),
    ]);
    health.value = healthStr;
    sessionHealth.value = sess;
  } finally {
    loading.value = false;
  }
}

onMounted(loadHealth);
</script>

<template>
  <div v-loading="loading" class="dashboard">
    <header class="dashboard__header">
      <div class="dashboard__brand">
        <span class="dashboard__brand-mark">E</span>
        <div class="dashboard__brand-text">
          <h1 class="dashboard__title">{{ t("executor.title") }}</h1>
          <p class="dashboard__subtitle">{{ t("executor.subtitle") }}</p>
        </div>
      </div>
      <div class="dashboard__actions">
        <LanguageSwitcher />
        <el-button class="dashboard__refresh" @click="loadHealth">
          {{ t("common.refresh") }}
        </el-button>
      </div>
    </header>

    <main class="dashboard__body">
      <HealthCard
        :health="health"
        :session-health="sessionHealth"
        :loading="loading"
      />

      <div class="dashboard__grid">
        <DeviceInfoCard :info="deviceInfo" />
        <section class="dashboard__card dashboard__card--device-extra">
          <h2 class="dashboard__card-title">{{ t("executor.uptime") }}</h2>
          <div class="dashboard__uptime">
            <span
              class="dashboard__pulse"
              :class="{ 'dashboard__pulse--ok': health }"
            />
            <span>{{ health || t("common.unknown") }}</span>
          </div>
        </section>
      </div>

      <section class="dashboard__card">
        <h2 class="dashboard__card-title">{{ t("executor.sessionsTitle") }}</h2>
        <SessionTable :sessions="sessions" />
      </section>
    </main>
  </div>
</template>

<style scoped>
/* dashboard 布局样式见 styles/dashboard.css（全局引入于 main.ts）。 */
</style>
