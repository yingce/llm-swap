export type ModelSignalTone = "good" | "warn" | "bad" | "neutral";

const runningStates = new Set(["active", "loaded", "ready", "running"]);
const transitionalStates = new Set(["installing", "loading", "pending", "starting", "warming"]);
const errorStates = new Set(["crashed", "error", "failed"]);

export function modelRuntimeLabel(state?: string): string {
  const normalized = normalizeState(state);
  if (!normalized) {
    return "not running";
  }
  return runningStates.has(normalized) ? "running" : normalized;
}

export function modelRuntimeTone(state?: string): ModelSignalTone {
  const normalized = normalizeState(state);
  if (runningStates.has(normalized)) {
    return "good";
  }
  if (transitionalStates.has(normalized)) {
    return "warn";
  }
  if (errorStates.has(normalized)) {
    return "bad";
  }
  return "neutral";
}

export function modelArtifactTone(state?: string): ModelSignalTone {
  const normalized = normalizeState(state);
  if (transitionalStates.has(normalized)) {
    return "warn";
  }
  if (errorStates.has(normalized)) {
    return "bad";
  }
  return "neutral";
}

export function modelRuntimeIsRunning(state?: string): boolean {
  return runningStates.has(normalizeState(state));
}

function normalizeState(state?: string): string {
  return String(state ?? "").trim().toLowerCase();
}
