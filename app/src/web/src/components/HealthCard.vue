<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import type { SessionHealth } from "@/api/types";

interface Props {
  /** Framework health string from GET /health (e.g. "ok", "healthy"). */
  health: string | null;
  /** Session health from GET /api/v1/addons/session/health. */
  sessionHealth: SessionHealth | null;
  loading: boolean;
}

const props = defineProps<Props>();
const { t } = useI18n();

/** "ok" / "healthy" / "up" → healthy; anything else non-empty → unhealthy. */
const isHealthy = computed<boolean | null>(() => {
  if (!props.health) return null;
  return /\b(ok|healthy|up|alive)\b/i.test(props.health);
});

const concurrencyPct = computed(() => {
  const max = props.sessionHealth?.maxSessions ?? 0;
  const cur = props.sessionHealth?.sessions ?? 0;
  if (max <= 0) return 0;
  return Math.min(100, Math.round((cur / max) * 100));
});

const healthLabel = computed(() => {
  if (isHealthy.value === null) return t("executor.healthUnknown");
  return isHealthy.value ? t("executor.healthOk") : t("executor.healthError");
});

const healthType = computed<"success" | "danger" | "info">(() => {
  if (isHealthy.value === null) return "info";
  return isHealthy.value ? "success" : "danger";
});

/** 映射到原生 tag 的 class（替代 el-tag 的 type）。 */
const healthTagClass = computed(() => ({
  "health-card__tag": true,
  "health-card__tag--success": healthType.value === "success",
  "health-card__tag--danger": healthType.value === "danger",
  "health-card__tag--info": healthType.value === "info",
}));
</script>

<template>
  <section class="health-card">
    <div class="health-card__top">
      <div class="health-card__metric">
        <span class="health-card__label">{{ t("executor.healthTitle") }}</span>
        <span :class="healthTagClass">{{ healthLabel }}</span>
      </div>
      <div class="health-card__divider" />
      <div class="health-card__metric health-card__metric--concurrency">
        <span class="health-card__label">{{ t("executor.sessionHealthTitle") }}</span>
        <div class="health-card__concurrency">
          <span class="health-card__concurrency-value">
            {{ sessionHealth?.sessions ?? t("common.na") }}
            <span class="health-card__concurrency-sep">/</span>
            {{ sessionHealth?.maxSessions ?? t("common.na") }}
          </span>
          <span class="health-card__concurrency-unit">
            {{ t("executor.concurrencyUnit") }}
          </span>
        </div>
        <div class="health-card__bar">
          <div class="health-card__bar-fill" :style="{ width: `${concurrencyPct}%` }" />
        </div>
      </div>
    </div>
  </section>
</template>

<style scoped>
.health-card {
  padding: 26px 28px;
  background: rgba(255, 255, 255, 0.92);
  backdrop-filter: blur(16px);
  border: 1px solid var(--na-border);
  border-radius: var(--na-r-xl);
  box-shadow: var(--na-shadow-md);
}

.health-card__top {
  display: flex;
  align-items: center;
  gap: 28px;
}

.health-card__metric {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: 10px;
}

.health-card__label {
  font-size: 12px;
  font-weight: 600;
  color: var(--na-text-secondary);
  text-transform: uppercase;
  letter-spacing: 0.05em;
}

.health-card__divider {
  width: 1px;
  align-self: stretch;
  background: var(--na-border);
}

.health-card__concurrency {
  display: flex;
  align-items: baseline;
  gap: 8px;
}

.health-card__concurrency-value {
  font-family: var(--na-font-display);
  font-size: 34px;
  color: var(--na-text);
}

.health-card__concurrency-sep {
  margin: 0 2px;
  color: var(--na-text-tertiary);
}

.health-card__concurrency-unit {
  font-size: 13px;
  color: var(--na-text-tertiary);
}

.health-card__bar {
  margin-top: 4px;
  max-width: 220px;
  height: 6px;
  border-radius: var(--r-full);
  background: var(--bg-subtle);
  overflow: hidden;
}

.health-card__bar-fill {
  height: 100%;
  border-radius: inherit;
  background: var(--grad-teal-indigo);
  transition: width 0.4s var(--ease);
}

/* 原生 tag（替代 el-tag） */
.health-card__tag {
  display: inline-block;
  padding: 4px 14px;
  border-radius: var(--r-full);
  font-size: 13px;
  font-weight: 600;
}
.health-card__tag--success { background: rgba(16, 185, 129, 0.15); color: var(--emerald-500); }
.health-card__tag--danger { background: rgba(244, 63, 94, 0.15); color: var(--rose-500); }
.health-card__tag--info { background: var(--bg-subtle); color: var(--text-tertiary); }
</style>
