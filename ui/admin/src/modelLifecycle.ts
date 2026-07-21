import type { ModelConfig, TagPolicyConfig } from "./api";

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
