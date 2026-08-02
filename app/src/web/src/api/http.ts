import axios from "axios";
import type { AxiosResponse } from "axios";
import type { ApiEnvelope } from "./types";

/**
 * Shared axios instance for the executor frontend.
 *
 * - baseURL comes from VITE_EXECUTOR_BACKEND_URL when set (e.g. a deployed
 *   executor backend); otherwise it is "/api" which the Vite dev server
 *   proxies to the prism-fusion backend on :6690 (see vite.config.ts). In
 *   production a reverse proxy handles the same path.
 * - The executor backend exposes no auth routes (see AGENTS.md), so no JWT is
 *   attached here.
 * - The response interceptor unwraps the unified `{ code, message, data }`
 *   envelope: code === 0 means success and we resolve with `data`; any other
 *   code is rejected with an `ApiError`. Endpoints without a `code` field
 *   (e.g. raw /health) are treated as pass-through successes.
 */
const baseURL = import.meta.env.VITE_EXECUTOR_BACKEND_URL ?? "/api";

const http = axios.create({
  baseURL,
  timeout: 15000,
  headers: {
    "Content-Type": "application/json",
  },
});

/** Error thrown when the envelope reports a business failure (code !== 0). */
export class ApiError extends Error {
  code: number;
  status: number;

  constructor(message: string, code: number, status: number) {
    super(message);
    this.name = "ApiError";
    this.code = code;
    this.status = status;
  }
}

http.interceptors.response.use(
  (response: AxiosResponse<ApiEnvelope<unknown>>) => {
    const envelope = response.data;
    // Some endpoints (e.g. raw /health) may not follow the envelope; treat a
    // missing `code` as a pass-through success.
    if (envelope === null || typeof envelope !== "object" || !("code" in envelope)) {
      return response;
    }

    if (envelope.code === 0) {
      // Resolve with the unwrapped payload so callers deal with `data` directly.
      return { ...response, data: envelope.data };
    }

    // Business failure: reject so callers can `.catch()` it.
    throw new ApiError(
      envelope.message || "Request failed",
      envelope.code,
      response.status,
    );
  },
  (error: unknown) => {
    // Network or HTTP-level error.
    if (axios.isAxiosError(error)) {
      const status = error.response?.status ?? 0;
      const envelope = error.response?.data as ApiEnvelope<unknown> | undefined;
      const message = envelope?.message || error.message || "Network error";
      return Promise.reject(new ApiError(message, envelope?.code ?? -1, status));
    }
    return Promise.reject(error);
  },
);

export default http;
