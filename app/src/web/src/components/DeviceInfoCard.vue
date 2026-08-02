<script setup lang="ts">
import { useI18n } from "vue-i18n";
import type { DeviceInfo } from "@/config/device";

defineProps<{ info: DeviceInfo }>();

const { t } = useI18n();
</script>

<template>
  <section class="device-card">
    <h2 class="device-card__title">{{ t("executor.deviceInfoTitle") }}</h2>
    <dl class="device-card__list">
      <div class="device-card__row">
        <dt>{{ t("executor.deviceId") }}</dt>
        <dd class="device-card__mono">{{ info.deviceId }}</dd>
      </div>
      <div class="device-card__row">
        <dt>{{ t("executor.instanceId") }}</dt>
        <dd class="device-card__mono">{{ info.instanceId }}</dd>
      </div>
      <div class="device-card__row">
        <dt>{{ t("executor.backendType") }}</dt>
        <dd>
          <el-tag size="small" type="info" effect="plain" round>
            {{ info.backendType }}
          </el-tag>
        </dd>
      </div>
    </dl>
  </section>
</template>

<style scoped>
.device-card {
  padding: 26px 28px;
  background: rgba(255, 255, 255, 0.92);
  backdrop-filter: blur(16px);
  border: 1px solid var(--na-border);
  border-radius: var(--na-r-xl);
  box-shadow: var(--na-shadow-md);
}

.device-card__title {
  margin: 0 0 18px;
  font-size: 15px;
  font-weight: 600;
  color: var(--na-text-secondary);
  text-transform: uppercase;
  letter-spacing: 0.05em;
}

.device-card__list {
  display: flex;
  flex-direction: column;
  gap: 14px;
  margin: 0;
}

.device-card__row {
  display: flex;
  align-items: center;
}

.device-card__row dt {
  width: 120px;
  margin: 0;
  color: var(--na-text-tertiary);
  font-size: 13px;
}

.device-card__row dd {
  margin: 0;
  font-size: 14px;
  color: var(--na-text);
}

.device-card__mono {
  font-family: var(--na-font-mono);
  word-break: break-all;
}
</style>
