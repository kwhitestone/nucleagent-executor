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

/** 映射到原生 tag 的 class（替代 el-tag 的 type）。 */
function statusTagClass(status: string): string {
  return `session-table__tag session-table__tag--${statusType(status)}`;
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
    <table class="session-table__el">
      <thead>
        <tr>
          <th>{{ t("executor.sessionId") }}</th>
          <th>{{ t("executor.status") }}</th>
          <th>{{ t("executor.backend") }}</th>
          <th>{{ t("executor.workdir") }}</th>
          <th>{{ t("executor.startedAt") }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-if="!rows.length">
          <td colspan="5" class="session-table__empty">{{ t("executor.sessionsEmpty") }}</td>
        </tr>
        <tr v-for="row in rows" :key="row.id">
          <td><span class="session-table__mono">{{ row.id }}</span></td>
          <td><span :class="statusTagClass(row.status)">{{ statusLabel(row.status) }}</span></td>
          <td>{{ row.backend || t("common.na") }}</td>
          <td><span class="session-table__mono">{{ row.workdir || t("common.na") }}</span></td>
          <td>{{ formatTime(row.startedAt) }}</td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<style scoped>
.session-table__el {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}
.session-table__el th {
  text-align: left;
  padding: 10px 12px;
  font-weight: 600;
  color: var(--text-secondary);
  border-bottom: 1px solid var(--border);
  background: var(--bg-subtle);
}
.session-table__el td {
  padding: 10px 12px;
  border-bottom: 1px solid var(--border);
  color: var(--text-primary);
}
.session-table__el tr:nth-child(even) td {
  background: var(--bg-subtle);
}
.session-table__empty {
  text-align: center;
  color: var(--text-tertiary);
  padding: 24px;
}

.session-table__mono {
  font-family: var(--font-mono);
  font-size: 13px;
  color: var(--text-secondary);
  word-break: break-all;
}

/* 原生状态 tag（替代 el-tag） */
.session-table__tag {
  display: inline-block;
  padding: 2px 10px;
  border-radius: var(--r-full);
  font-size: 11px;
  font-weight: 600;
}
.session-table__tag--primary { background: rgba(99, 102, 241, 0.15); color: var(--indigo-500); }
.session-table__tag--success { background: rgba(16, 185, 129, 0.15); color: var(--emerald-500); }
.session-table__tag--danger { background: rgba(244, 63, 94, 0.15); color: var(--rose-500); }
.session-table__tag--warning { background: rgba(245, 158, 11, 0.15); color: var(--amber-600); }
.session-table__tag--info { background: var(--bg-subtle); color: var(--text-tertiary); }
</style>
