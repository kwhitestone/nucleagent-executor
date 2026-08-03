/**
 * Shared API type definitions matching the nucleagent-executor backend contract.
 *
 * The executor is a prism-fusion service on :26690. Most endpoints (register,
 * heartbeat, WebSocket) are server-to-server and not exposed to this frontend.
 * The web UI only consumes two health endpoints:
 *   GET /health                           — framework health check (plain text/JSON)
 *   GET /api/v1/addons/session/health     — session health: { status, sessions, max_sessions }
 *
 * The framework wraps responses in a unified envelope `{ code, message, data }`;
 * the raw /health probe and some pass-through endpoints may not, so the http
 * layer treats a missing `code` field as a pass-through success.
 */

/** Unified envelope returned by prism-fusion endpoints: { code, message, data } */
export interface ApiEnvelope<T> {
  code: number;
  message: string;
  data: T;
}

/**
 * Session health payload returned by GET /api/v1/addons/session/health.
 * Field names mirror the a2a session store contract (snake_case over the wire).
 */
export interface SessionHealth {
  status: string;
  sessions: number;
  maxSessions: number;
}

/** Raw shape from the backend before camelCase normalization. */
export interface SessionHealthRaw {
  status: string;
  sessions: number;
  max_sessions: number;
}

/** A single task session row for the placeholder session list. */
export interface TaskSession {
  id: string;
  status: string;
  workdir: string;
  backend: string;
  startedAt: number;
}
