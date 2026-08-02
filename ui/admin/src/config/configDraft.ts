import YAML from "yaml";

import type { ConfigResponse, GatewayConfigView, ModelConfig, TagPolicyConfig } from "../api";
import type { EditableModelConfig } from "../modelLifecycle";

export type EditableGatewayConfig = {
  models: Record<string, EditableModelConfig>;
  model_aliases: Record<string, string>;
  tag_policies: Record<string, TagPolicyConfig>;
};

export function hasLegacyCapacityCeiling(maxConcurrency: number, maxQueue: number): boolean {
  return maxConcurrency > 0 || maxQueue > 0;
}

export function normalizeTagPolicy(policy: TagPolicyConfig): TagPolicyConfig {
  return {
    ...policy,
    worker_defaults: {
      max_concurrency: policy.worker_defaults?.max_concurrency ?? 0,
      max_queue: policy.worker_defaults?.max_queue ?? 0
    },
    allowed_models: [...(policy.allowed_models ?? [])].sort()
  };
}

export function cloneEditableConfig(config: EditableGatewayConfig): EditableGatewayConfig {
  return {
    models: Object.fromEntries(
      Object.entries(config.models).map(([name, model]) => [
        name,
        {
          ...model,
          artifact: { ...model.artifact },
          runtime_args: [...model.runtime_args],
          tag_capacity: cloneTagCapacity(model.tag_capacity),
          disabled: model.disabled || undefined,
          billing: model.billing ? { ...model.billing } : undefined
        }
      ])
    ),
    model_aliases: { ...config.model_aliases },
    tag_policies: Object.fromEntries(
      Object.entries(config.tag_policies).map(([name, policy]) => [
        name,
        {
          ...policy,
          worker_defaults: { ...policy.worker_defaults },
          allowed_models: [...policy.allowed_models]
        }
      ])
    )
  };
}

export function toEditableConfig(configResponse: ConfigResponse): EditableGatewayConfig {
  const parsed = YAML.parseDocument(configResponse.yaml);
  const editableModels = Object.fromEntries(
    Object.entries(configResponse.config.models ?? {}).map(([name, model]) => [
      name,
      {
        ...model,
        artifact: { ...model.artifact },
        runtime_args: [...(model.runtime_args ?? [])],
        tag_capacity: cloneTagCapacity(model.tag_capacity),
        disabled: model.disabled || undefined,
        billing: model.billing ? { ...model.billing } : undefined,
        max_loaded_auto: !yamlModelHasKey(parsed, name, "max_loaded") && model.max_loaded === 0
      }
    ])
  );
  const editableTagPolicies = Object.fromEntries(
    Object.entries(configResponse.config.tag_policies ?? {}).map(([name, policy]) => [name, normalizeTagPolicy(policy)])
  );
  return {
    models: editableModels,
    model_aliases: { ...(configResponse.config.model_aliases ?? {}) },
    tag_policies: editableTagPolicies
  };
}

export function renderDraftYAML(baseYaml: string, draft: EditableGatewayConfig) {
  const document = YAML.parseDocument(baseYaml);
  const rendered = toGatewayConfigView(draft);
  document.set("models", createYamlModelsMap(rendered.models, draft.models));
  const sortedAliases = Object.fromEntries(
    Object.entries(rendered.model_aliases).sort(([a], [b]) => a.localeCompare(b))
  );
  if (Object.keys(sortedAliases).length > 0) {
    document.set("model_aliases", sortedAliases);
  } else {
    document.delete("model_aliases");
  }
  document.set("tag_policies", rendered.tag_policies);
  return String(document);
}

function yamlModelHasKey(document: YAML.Document.Parsed, modelName: string, field: string) {
  const modelsNode = document.get("models", true) as any;
  const modelNode = modelsNode?.items?.find((item: any) => item?.key?.value === modelName)?.value;
  return Boolean(modelNode?.items?.some((item: any) => item?.key?.value === field));
}

function toGatewayConfigView(draft: EditableGatewayConfig): GatewayConfigView {
  return {
    models: Object.fromEntries(
      Object.entries(draft.models).map(([name, model]) => {
        const nextModel: ModelConfig = {
          disabled: model.disabled || undefined,
          priority: model.priority,
          min_loaded: model.min_loaded,
          max_loaded: model.max_loaded_auto ? 0 : model.max_loaded,
          max_concurrency: model.max_concurrency,
          max_queue: model.max_queue,
          tag_capacity: cloneTagCapacity(model.tag_capacity),
          queue_timeout_ms: model.queue_timeout_ms,
          ttl: model.ttl,
          model_dir: model.model_dir?.trim() || undefined,
          artifact: { ...model.artifact },
          run: model.run,
          runtime: model.runtime,
          runtime_args: [...model.runtime_args],
          cmd_stop: model.cmd_stop,
          check_endpoint: model.check_endpoint,
          billing: model.billing ? { ...model.billing } : undefined
        };
        return [name, nextModel];
      })
    ),
    model_aliases: Object.fromEntries(
      Object.entries(draft.model_aliases).sort(([a], [b]) => a.localeCompare(b))
    ),
    tag_policies: Object.fromEntries(
      Object.entries(draft.tag_policies).map(([name, policy]) => [
        name,
        {
          ...policy,
          worker_defaults: { ...policy.worker_defaults },
          allowed_models: [...policy.allowed_models].sort()
        }
      ])
    )
  };
}

function createYamlModelsMap(
  models: Record<string, ModelConfig>,
  editableModels: Record<string, EditableModelConfig>
) {
  return Object.fromEntries(
    Object.entries(models).map(([name, model]) => {
      const editable = editableModels[name];
      const nextModel: Record<string, unknown> = {
        priority: model.priority,
        min_loaded: model.min_loaded,
        max_concurrency: model.max_concurrency,
        max_queue: model.max_queue,
        queue_timeout_ms: model.queue_timeout_ms,
        ttl: model.ttl
      };
      if (model.tag_capacity && Object.keys(model.tag_capacity).length > 0) {
        nextModel.tag_capacity = Object.fromEntries(
          Object.entries(model.tag_capacity)
            .sort(([left], [right]) => left.localeCompare(right))
            .map(([tag, capacity]) => [tag, { ...capacity }])
        );
      }
      const modelDir = model.model_dir?.trim();
      if (modelDir) {
        nextModel.model_dir = modelDir;
      }
      nextModel.artifact = { ...model.artifact };
      if (model.disabled) {
        nextModel.disabled = true;
      }
      if (!editable.max_loaded_auto) {
        nextModel.max_loaded = model.max_loaded;
      }
      if (model.run) {
        nextModel.run = model.run;
      }
      if (model.runtime) {
        nextModel.runtime = model.runtime;
      }
      if (model.runtime_args && model.runtime_args.length > 0) {
        nextModel.runtime_args = model.runtime_args;
      }
      if (model.cmd_stop) {
        nextModel.cmd_stop = model.cmd_stop;
      }
      if (model.check_endpoint) {
        nextModel.check_endpoint = model.check_endpoint;
      }
      if (model.billing && Object.keys(model.billing).length > 0) {
        nextModel.billing = { ...model.billing };
      }
      return [name, nextModel];
    })
  );
}

function cloneTagCapacity(values?: Record<string, { max_concurrency: number; max_queue: number }>) {
  if (!values) return {};
  return Object.fromEntries(Object.entries(values).map(([tag, capacity]) => [tag, { ...capacity }]));
}
