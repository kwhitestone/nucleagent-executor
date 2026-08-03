<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import { getHealth, getSessionHealth } from "@/api/executor";
import type { SessionHealth } from "@/api/types";
import { getDeviceInfo } from "@/config/device";
import { mockSessions } from "@/mock/sessions";
import { toast } from "@/composables/useToast";
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
        toast.error(err instanceof Error ? err.message : t("executor.loadHealthFailed"));
        return null;
      }),
      getSessionHealth().catch((err: unknown) => {
        toast.error(err instanceof Error ? err.message : t("executor.loadSessionHealthFailed"));
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
  <div class="dashboard">
    <div class="dashboard__toolbar">
      <button class="dashboard__refresh" type="button" @click="loadHealth">
        {{ t("common.refresh") }}
      </button>
    </div>

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
