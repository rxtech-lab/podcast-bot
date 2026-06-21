import type { RJSFSchema, UiSchema } from "@rjsf/utils";

// agentSpec is reused for host / commander / discussants. modelEnum is injected
// at runtime from GET /api/models (see withModelEnum below).
function agentSpec(withAspect: boolean): RJSFSchema {
  const props: Record<string, RJSFSchema> = {
    name: { type: "string", title: "Name" },
    model: { type: "string", title: "Model", enum: [] },
  };
  if (withAspect) {
    props.aspect = { type: "string", title: "Aspect (the angle they argue from)" };
  }
  return {
    type: "object",
    required: ["model"],
    properties: props,
  };
}

export const discussionSchema: RJSFSchema = {
  type: "object",
  required: ["title", "type", "language", "channel", "host", "discussants", "commander"],
  properties: {
    title: { type: "string", title: "Title" },
    type: { type: "string", title: "Type", const: "discussion", default: "discussion" },
    language: { type: "string", title: "Language", default: "en-US" },
    channel: { type: "string", title: "Channel", default: "default" },
    total_minutes: { type: "integer", title: "Total minutes", default: 30, minimum: 1 },
    segment_max_seconds: { type: "integer", title: "Max segment seconds", default: 60, minimum: 5 },
    tts_provider: { type: "string", title: "TTS provider", enum: ["azure", "eleven"], default: "azure" },
    resolution: { type: "string", title: "Resolution", enum: ["720p", "1080p", "4k"], default: "1080p" },
    storage: { type: "string", title: "Research storage", enum: ["plaintext", "mongodb"], default: "plaintext" },
    host: { ...agentSpec(false), title: "Host (moderator)" },
    discussants: {
      type: "array",
      title: "Discussants",
      minItems: 2,
      items: { ...agentSpec(true), required: ["name", "model"] },
    },
    commander: { ...agentSpec(false), title: "Commander (silent visual/music director)" },
    background: { type: "string", title: "Background" },
  },
};

export const discussionUiSchema: UiSchema = {
  type: { "ui:widget": "hidden" },
  background: { "ui:widget": "textarea", "ui:options": { rows: 6 } },
  host: {
    model: { "ui:widget": "select" },
  },
  commander: {
    name: { "ui:widget": "hidden" },
    model: { "ui:widget": "select" },
  },
  discussants: {
    items: {
      model: { "ui:widget": "select" },
    },
  },
  "ui:submitButtonOptions": { norender: true },
};

// withModelEnum returns a copy of the schema with every `*.model` enum filled
// from the engine's model ids, so the selects show the real catalogue.
export function withModelEnum(modelIds: string[]): RJSFSchema {
  const clone = structuredClone(discussionSchema);
  const props = clone.properties as Record<string, RJSFSchema>;
  const setEnum = (s: RJSFSchema | undefined) => {
    if (s?.properties && "model" in s.properties) {
      (s.properties.model as RJSFSchema).enum = modelIds;
    }
  };
  setEnum(props.host as RJSFSchema);
  setEnum(props.commander as RJSFSchema);
  const discussants = props.discussants as RJSFSchema;
  setEnum(discussants.items as RJSFSchema);
  return clone;
}
