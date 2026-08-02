/// <reference types="vite/client" />

interface ImportMetaEnv {
  /** Executor backend base URL (e.g. http://localhost:6690). Falls back to /api. */
  readonly VITE_EXECUTOR_BACKEND_URL?: string;
  /** Logical device id (config.yaml nucleagent.device-id). */
  readonly VITE_EXECUTOR_DEVICE_ID?: string;
  /** Stable instance id (config.yaml nucleagent.instance-id). */
  readonly VITE_EXECUTOR_INSTANCE_ID?: string;
  /** Default execution backend (config.yaml nucleagent.backend). */
  readonly VITE_EXECUTOR_BACKEND?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}

declare module "*.vue" {
  import type { DefineComponent } from "vue";
  const component: DefineComponent<Record<string, unknown>, Record<string, unknown>, unknown>;
  export default component;
}
