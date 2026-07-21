import type { ModelConfig, TagPolicyConfig, WorkerStatus } from "./api";

export type EditableModelConfig = Omit<ModelConfig, "runtime_args"> & {
  runtime_args: string[];
  max_loaded_auto: boolean;
};

export const MODEL_RUNTIME_OPTIONS = ["vllm", "sglang", "llamacpp"] as const;

export type ModelCreateDraft = {
  name: string;
  model: EditableModelConfig;
  tags: string[];
};

export type ModelDeleteBlockers = {
  aliases: string[];
  tags: string[];
  running: Array<{ workerID: string; state: string }>;
};

export function emptyEditableModel(): EditableModelConfig {
  return {
    disabled: true,
    model_dir: undefined,
    priority: 0,
    min_loaded: 0,
    max_loaded: 0,
    max_loaded_auto: true,
    max_concurrency: 0,
    max_queue: 0,
    queue_timeout_ms: 0,
    ttl: 0,
    artifact: { object: "", kind: "tar_gz", crc64ecma: "" },
    run: "",
    runtime: "vllm",
    runtime_args: []
  };
}

export function copyEditableModel(source: EditableModelConfig): EditableModelConfig {
  return {
    ...source,
    artifact: { ...source.artifact },
    runtime_args: [...source.runtime_args],
    billing: source.billing ? { ...source.billing } : undefined,
    disabled: true,
    min_loaded: 0
  };
}

export function isModelCreateDraftDirty(initial: ModelCreateDraft, current: ModelCreateDraft) {
  return initial.name !== current.name
    || initial.tags.join("\u0000") !== current.tags.join("\u0000")
    || JSON.stringify(initial.model) !== JSON.stringify(current.model);
}

export function validateNewModelName(
  name: string,
  models: Record<string, unknown>,
  aliases: Record<string, string>
): string {
  const trimmed = name.trim();
  if (!trimmed) return "Model name is required.";
  if (Object.hasOwn(models, trimmed)) return "Model name already exists.";
  if (Object.hasOwn(aliases, trimmed)) return "Model name collides with an alias.";
  return "";
}

export function setModelTagMembership(
  policies: Record<string, TagPolicyConfig>,
  model: string,
  selected: string[]
): Record<string, TagPolicyConfig> {
  const selectedSet = new Set(selected);
  return Object.fromEntries(Object.entries(policies).map(([tag, policy]) => {
    const allowed = policy.allowed_models.filter((name) => name !== model);
    if (selectedSet.has(tag)) allowed.push(model);
    return [tag, {
      ...policy,
      worker_defaults: { ...policy.worker_defaults },
      allowed_models: [...new Set(allowed)].sort()
    }];
  }));
}

export function modelDeleteBlockers(
  model: string,
  aliases: Record<string, string>,
  policies: Record<string, TagPolicyConfig>,
  workers: WorkerStatus[]
): ModelDeleteBlockers {
  return {
    aliases: Object.keys(aliases).filter((alias) => aliases[alias] === model).sort(),
    tags: Object.keys(policies).filter((tag) => policies[tag].allowed_models.includes(model)).sort(),
    running: workers.flatMap((worker) => worker.running_models
      .filter((entry) => entry.model === model)
      .map((entry) => ({ workerID: worker.id, state: entry.state })))
      .sort((a, b) => a.workerID.localeCompare(b.workerID) || a.state.localeCompare(b.state))
  };
}
