import { describe, expect, it } from "vitest";
import { normalizeStatus, type StatusResponse, type WorkerStatus } from "./api";

const statusFixture = {
  generated_at: "2026-07-31T06:00:00Z",
  summary: {
    total_workers: 1,
    healthy_workers: 1,
    draining_workers: 0,
    available_models: 1,
    configured_models: 1,
    underprovisioned_models: 1,
    active_requests: 0,
    stale_workers: 0,
    workers_with_errors: 0,
    recent_error_events: 0
  },
  models: [
    {
      name: "qwen",
      priority: 10,
      min_loaded: 2,
      max_loaded: 3,
      active_requests: 1,
      max_concurrency: 4,
      queued_requests: 2,
      max_queue: 8,
      queue_timeout_ms: 1500,
      ttl: 300,
      available: true,
      ready_workers: 1,
      running_workers: 1,
      installing_workers: 1,
      missing_workers: 2,
      error_workers: 3,
      artifact: { object: "models/qwen.tar.gz", kind: "tar_gz" },
      availability_note: "installing",
      traffic: {
        requests: 5,
        status_2xx: 4,
        status_4xx: 1,
        status_5xx: 0,
        prompt_tokens: 100,
        completion_tokens: 50,
        total_tokens: 150,
        cache_tokens: 10,
        reasoning_tokens: 20,
        avg_duration_ms: 250,
        max_duration_ms: 500
      },
      worker_statuses: [
        {
          worker_id: "worker-a",
          artifact_status: "ready",
          running_state: "ready",
          health: "healthy",
          cooldown_active: true,
          cooldown_reason: "upstream_503",
          cooldown_remaining_seconds: 18,
          cooldown_until: "2026-07-31T06:00:18Z"
        }
      ]
    }
  ],
  workers: [
    {
      id: "worker-a",
      tags: ["gpu-4090"],
      health: "healthy",
      state: "active",
      llama_swap_url: "http://worker-a:6006",
      last_heartbeat: "2026-07-31T06:00:00Z",
      last_heartbeat_age_ms: 25,
      active_requests: 0,
      live_capacity_available: true,
      queued_requests: 0,
      running_models: [{ model: "qwen", state: "ready" }],
      gpu_devices: [],
      allowed_models: ["qwen"],
      artifacts: { qwen: "ready" },
      capacity: { max_concurrency: 4, max_queue: 8 },
      needs_restart: false,
      scrape_failures: 3,
      scrape_backoff_until: "2026-07-31T06:01:00Z",
      scrape_backoff_seconds: 60,
      replica_cooldowns: [
        {
          worker_id: "worker-a",
          model: "qwen",
          reason: "upstream_503",
          first_failure: "2026-07-31T05:59:30Z",
          last_failure: "2026-07-31T06:00:00Z",
          failure_count: 2,
          cooldown_until: "2026-07-31T06:00:18Z",
          remaining_seconds: 18
        }
      ],
      agent_build: { version: "1.2.3", commit: "abc123" },
      agent_version_status: "current"
    }
  ],
  events: [
    {
      received_at: "2026-07-31T06:00:01Z",
      worker_id: "worker-a",
      time: "2026-07-31T06:00:00Z",
      event: "artifact_state",
      kind: "tar_gz",
      model: "qwen"
    }
  ]
} satisfies StatusResponse;

describe("normalizeStatus", () => {
  it("preserves provisioning, cooldown, scrape backoff, and event fields", () => {
    const normalized = normalizeStatus(statusFixture);

    expect(normalized.models[0]).toMatchObject({
      queue_timeout_ms: 1500,
      ttl: 300,
      installing_workers: 1,
      missing_workers: 2,
      error_workers: 3
    });
    expect(normalized.models[0].worker_statuses[0]).toMatchObject({
      cooldown_reason: "upstream_503",
      cooldown_remaining_seconds: 18,
      cooldown_until: "2026-07-31T06:00:18Z"
    });
    expect(normalized.workers[0]).toMatchObject({
      live_capacity_available: true,
      scrape_backoff_until: "2026-07-31T06:01:00Z",
      replica_cooldowns: statusFixture.workers[0].replica_cooldowns
    });
    expect(normalized.events[0]).toMatchObject({
      time: "2026-07-31T06:00:00Z",
      kind: "tar_gz"
    });
  });

  it("normalizes a missing historical replica cooldown list to an empty array", () => {
    const legacyStatus = structuredClone(statusFixture) as StatusResponse;
    delete (legacyStatus.workers[0] as Partial<WorkerStatus>).replica_cooldowns;

    expect(normalizeStatus(legacyStatus).workers[0].replica_cooldowns).toEqual([]);
  });

  it("normalizes missing live capacity availability from an older gateway to false", () => {
    const legacyStatus = structuredClone(statusFixture) as StatusResponse;
    delete (legacyStatus.workers[0] as Partial<WorkerStatus>).live_capacity_available;

    expect(normalizeStatus(legacyStatus).workers[0].live_capacity_available).toBe(false);
  });
});
