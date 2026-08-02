<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import type { TaskSession } from "@/api/types";

const props = defineProps<{ sessions: TaskSession[] }>();
const { t } = useI18n();

type StatusType = "primary" | "success" | "warning" | "danger" | "info";

function statusType(status: string): StatusType {
  switch (status) {
    case "running":
      return "primary";
    case "done":
      return "success";
    case "failed":
      return "danger";
    case "killed":
      return "warning";
    default:
      return "info";
  }
}

function statusLabel(status: string): string {
  switch (status) {
    case "running":
      return t("executor.statusRunning");
    case "done":
      return t("executor.statusDone");
    case "failed":
      return t("executor.statusFailed");
    case "killed":
      return t("executor.statusKilled");
    default:
      return status || t("common.unknown");
  }
}

function formatTime(ms: number): string {
  if (!ms) return t("common.na");
  return new Date(ms).toLocaleTimeString();
}

const rows = computed(() => props.sessions);
</script>

<template>
  <div class="session-table">
    <el-table
      :data="rows"
      :empty-text="t('executor.sessionsEmpty')"
      stripe
      style="width: 100%"
    >
      <el-table-column :label="t('executor.sessionId')" min-width="240">
        <template #default="{ row }">
          <span class="session-table__mono">{{ row.id }}</span>
        </template>
      </el-table-column>
      <el-table-column :label="t('executor.status')" width="120">
        <template #default="{ row }">
          <el-tag :type="statusType(row.status)" size="small" effect="light" round>
            {{ statusLabel(row.status) }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column :label="t('executor.backend')" width="120">
        <template #default="{ row }">{{ row.backend || t("common.na") }}</template>
      </el-table-column>
      <el-table-column :label="t('executor.workdir')" min-width="200">
        <template #default="{ row }">
          <span class="session-table__mono">{{ row.workdir || t("common.na") }}</span>
        </template>
      </el-table-column>
      <el-table-column :label="t('executor.startedAt')" width="120">
        <template #default="{ row }">{{ formatTime(row.startedAt) }}</template>
      </el-table-column>
    </el-table>
  </div>
</template>

<style scoped>
.session-table__mono {
  font-family: var(--na-font-mono);
  font-size: 13px;
  color: var(--na-text-secondary);
  word-break: break-all;
}
</style>
