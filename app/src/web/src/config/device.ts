/**
 * Device / instance descriptors shown on the dashboard.
 *
 * These values normally come from the executor's runtime config (config.yaml
 * `nucleagent` section: device-id, instance-id, backend). The web frontend does
 * not have direct access to that config, so they are read from build-time env
 * (VITE_EXECUTOR_*) with sensible placeholders for local dev.
 */
export interface DeviceInfo {
  deviceId: string;
  instanceId: string;
  backendType: string;
}

function envOr(key: string, fallback: string): string {
  const value = import.meta.env[key] as string | undefined;
  return value && value.length > 0 ? value : fallback;
}

export function getDeviceInfo(): DeviceInfo {
  return {
    deviceId: envOr("VITE_EXECUTOR_DEVICE_ID", "nucleagent-executor"),
    instanceId: envOr("VITE_EXECUTOR_INSTANCE_ID", "local-dev-instance"),
    backendType: envOr("VITE_EXECUTOR_BACKEND", "mock-llm"),
  };
}
