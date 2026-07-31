import type { StatusResponse } from "../api";

export function createStatusFixture(): StatusResponse {
  return {
    generated_at: "2026-07-31T08:00:00Z",
    summary: {
      total_workers: 3,
      healthy_workers: 1,
      draining_workers: 1,
      available_models: 1,
      configured_models: 2,
      underprovisioned_models: 1,
      active_requests: 3,
      stale_workers: 1,
      workers_with_errors: 1,
      recent_error_events: 1
    },
    models: [
      {
        name: "joyfox-model-latest",
        priority: 10,
        min_loaded: 2,
        max_loaded: 3,
        max_concurrency: 8,
        max_queue: 16,
        queue_timeout_ms: 2000,
        ttl: 300,
        available: true,
        ready_workers: 1,
        running_workers: 2,
        installing_workers: 1,
        missing_workers: 1,
        error_workers: 0,
        artifact: { object: "joyfox/latest.tar.gz", kind: "tar_gz" },
        availability_note: "one ready replica, one installing replica",
        traffic: {
          requests: 12,
          status_2xx: 11,
          status_4xx: 1,
          status_5xx: 0,
          prompt_tokens: 1200,
          completion_tokens: 600,
          total_tokens: 1800,
          cache_tokens: 100,
          reasoning_tokens: 50,
          avg_duration_ms: 420,
          max_duration_ms: 980
        },
        worker_statuses: [
          {
            worker_id: "worker-a",
            artifact_status: "ready",
            running_state: "ready",
            health: "healthy",
            cooldown_active: false
          },
          {
            worker_id: "worker-b",
            artifact_status: "ready",
            running_state: "installing",
            health: "healthy",
            cooldown_active: true,
            cooldown_reason: "upstream_503",
            cooldown_remaining_seconds: 20,
            cooldown_until: "2026-07-31T08:00:20Z"
          }
        ]
      },
      {
        name: "embedding-idle",
        priority: 1,
        min_loaded: 0,
        max_loaded: 1,
        max_concurrency: 2,
        max_queue: 4,
        queue_timeout_ms: 1000,
        ttl: 120,
        available: false,
        ready_workers: 0,
        running_workers: 0,
        installing_workers: 0,
        missing_workers: 0,
        error_workers: 0,
        artifact: { object: "embedding/idle.gguf", kind: "file" },
        availability_note: "cold by design",
        traffic: {
          requests: 0,
          status_2xx: 0,
          status_4xx: 0,
          status_5xx: 0,
          prompt_tokens: 0,
          completion_tokens: 0,
          total_tokens: 0,
          cache_tokens: 0,
          reasoning_tokens: 0,
          avg_duration_ms: 0,
          max_duration_ms: 0
        },
        worker_statuses: []
      }
    ],
    workers: [
      {
        id: "worker-a",
        tags: ["gpu-4090"],
        health: "healthy",
        state: "active",
        llama_swap_url: "http://worker-a:6006",
        last_heartbeat: "2026-07-31T08:00:00Z",
        last_heartbeat_age_ms: 2000,
        active_requests: 2,
        running_models: [{ model: "joyfox-model-latest", state: "ready" }],
        gpu_devices: [
          {
            index: 0,
            name: "NVIDIA GeForce RTX 4090",
            memory_total_mib: 24564,
            memory_used_mib: 16000,
            memory_free_mib: 8564,
            utilization_percent: 80,
            temperature_celsius: 70
          }
        ],
        allowed_models: ["joyfox-model-latest", "embedding-idle"],
        artifacts: { "joyfox-model-latest": "ready" },
        capacity: { max_concurrency: 4, max_queue: 8 },
        scrape_failures: 0,
        replica_cooldowns: [],
        agent_build: { version: "1.0.0", commit: "abc123" },
        agent_version_status: "current"
      },
      {
        id: "worker-b",
        tags: ["gpu-4090", "self-4090"],
        health: "healthy",
        state: "draining",
        llama_swap_url: "http://worker-b:6006",
        last_heartbeat: "2026-07-31T07:59:58Z",
        last_heartbeat_age_ms: 4000,
        active_requests: 1,
        running_models: [{ model: "joyfox-model-latest", state: "installing" }],
        gpu_devices: [
          {
            index: 0,
            name: "NVIDIA GeForce RTX 4090",
            memory_total_mib: 24564,
            memory_used_mib: 8000,
            memory_free_mib: 16564,
            utilization_percent: 30,
            temperature_celsius: 55
          }
        ],
        allowed_models: ["joyfox-model-latest"],
        artifacts: { "joyfox-model-latest": "ready" },
        capacity: { max_concurrency: 4, max_queue: 8 },
        scrape_failures: 2,
        scrape_backoff_until: "2026-07-31T08:01:00Z",
        scrape_backoff_seconds: 60,
        replica_cooldowns: [
          {
            worker_id: "worker-b",
            model: "joyfox-model-latest",
            reason: "upstream_503",
            first_failure: "2026-07-31T07:59:00Z",
            last_failure: "2026-07-31T08:00:00Z",
            failure_count: 3,
            cooldown_until: "2026-07-31T08:00:20Z",
            remaining_seconds: 20
          }
        ],
        agent_build: { version: "1.0.0", commit: "def456" },
        agent_version_status: "current"
      },
      {
        id: "worker-c",
        tags: ["self-4090"],
        health: "stale",
        state: "active",
        llama_swap_url: "http://worker-c:6006",
        last_heartbeat: "2026-07-31T07:55:00Z",
        last_heartbeat_age_ms: 300000,
        active_requests: 0,
        running_models: [],
        gpu_devices: [],
        allowed_models: ["embedding-idle"],
        artifacts: {},
        capacity: { max_concurrency: 2, max_queue: 4 },
        needs_restart: true,
        last_error: "heartbeat timeout",
        scrape_failures: 0,
        replica_cooldowns: [],
        agent_build: { version: "0.9.0", commit: "old999" },
        agent_version_status: "outdated"
      }
    ],
    events: [
      {
        received_at: "2026-07-31T08:00:01Z",
        worker_id: "worker-b",
        time: "2026-07-31T08:00:00Z",
        event: "model_error",
        model: "joyfox-model-latest",
        error: "upstream returned 503"
      }
    ]
  };
}
