import type { TaskSession } from "@/api/types";

/**
 * Placeholder task sessions for the executor dashboard.
 *
 * The executor backend does not yet expose a "list sessions" HTTP endpoint to
 * the frontend (sessions flow over the core WebSocket), so the session table is
 * populated from mock data until that endpoint lands. Swap `loadSessions()` for
 * a real `getSessions()` call in src/api/executor.ts once available.
 */
export const mockSessions: TaskSession[] = [
  {
    id: "550e8400-e29b-41d4-a716-446655440000",
    status: "running",
    workdir: "/sandboxes/sess-aa1f",
    backend: "mock-llm",
    startedAt: Date.now() - 1000 * 60 * 4,
  },
  {
    id: "6ba7b810-9dad-11d1-80b4-00c04fd430c8",
    status: "done",
    workdir: "/sandboxes/sess-92c0",
    backend: "opencode",
    startedAt: Date.now() - 1000 * 60 * 42,
  },
  {
    id: "f47ac10b-58cc-4372-a567-0e02b2c3d479",
    status: "failed",
    workdir: "/sandboxes/sess-d31e",
    backend: "opencode",
    startedAt: Date.now() - 1000 * 60 * 90,
  },
];
