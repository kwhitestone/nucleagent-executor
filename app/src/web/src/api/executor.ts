import http from "./http";
import type { SessionHealth, SessionHealthRaw } from "./types";

const SESSION_BASE = "/api/v1/addons/session";

/**
 * GET /health — prism-fusion framework health check.
 *
 * Returns a plain-text/JSON status (no unified envelope), so the http layer
 * passes it through untouched. We coerce to a trimmed string for the UI.
 */
export async function getHealth(): Promise<string> {
  const response = await http.get<unknown>("/health");
  const data = response.data;
  if (typeof data === "string") return data.trim();
  // Some frameworks JSON-encode the health payload; surface a stable value.
  if (data !== null && typeof data === "object" && "status" in data) {
    return String((data as { status: unknown }).status);
  }
  return String(data);
}

/**
 * GET /api/v1/addons/session/health — session addon health.
 *
 * Returns the envelope-unwrapped data: { status, sessions, max_sessions }.
 * Normalized to camelCase so the rest of the app deals with TS-friendly keys.
 */
export async function getSessionHealth(): Promise<SessionHealth> {
  const response = await http.get<SessionHealthRaw>(`${SESSION_BASE}/health`);
  const raw = response.data;
  return {
    status: raw.status,
    sessions: raw.sessions,
    maxSessions: raw.max_sessions,
  };
}
