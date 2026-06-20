"use client";

import { Handle, Position, type NodeProps } from "@xyflow/react";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";
import { useNodeEdit } from "@/lib/flow/node-context";
import type { AgentNodeData } from "@/lib/flow/graph";

const KIND_STYLES: Record<string, string> = {
  orchestrator: "border-primary/60 bg-primary/5",
  host: "border-blue-400/60 bg-blue-50/40 dark:bg-blue-950/20",
  discussant: "border-emerald-400/60 bg-emerald-50/40 dark:bg-emerald-950/20",
  commander: "border-purple-400/60 bg-purple-50/40 dark:bg-purple-950/20",
  viewer: "border-amber-400/60 bg-amber-50/40 dark:bg-amber-950/20",
};

const ACTIVITY_LABEL: Record<string, string> = {
  searching: "🔎 searching",
  memory: "📝 memory",
  speaking: "🎙 speaking",
  directing: "🎬 directing",
  thinking: "💭 thinking",
  idle: "· idle",
};

// States that warrant the attention-grabbing pulse ring. "thinking" is a
// passive standby state, so it gets a subtler treatment instead.
const ACTIVE_STATES = new Set([
  "searching",
  "memory",
  "speaking",
  "directing",
]);

export function AgentFlowNode({ data }: NodeProps) {
  const d = data as AgentNodeData;
  const { readOnly, models, onPatch } = useNodeEdit();
  const editable = !readOnly && d.kind !== "orchestrator";

  return (
    <div
      className={cn(
        "min-w-[170px] max-w-[260px] rounded-lg border-2 px-3 py-2 shadow-sm text-xs",
        KIND_STYLES[d.kind] ?? "border-border bg-card",
        d.activity && ACTIVE_STATES.has(d.activity) && "ring-2 ring-ring animate-pulse",
        d.activity === "thinking" && "ring-1 ring-muted-foreground/40",
      )}
    >
      <Handle type="target" position={Position.Top} className="!bg-muted-foreground" />
      <div className="flex items-start justify-between gap-1">
        <span className="font-semibold capitalize">{d.kind}</span>
        {d.activity ? (
          <span className="min-w-0 break-words text-right text-[10px] text-muted-foreground">
            <span className="whitespace-nowrap">
              {ACTIVITY_LABEL[d.activity] ?? d.activity}
            </span>
            {d.activityDetail ? ` · ${d.activityDetail}` : ""}
          </span>
        ) : null}
      </div>

      {d.kind === "orchestrator" ? (
        <div className="mt-1 text-[11px] text-muted-foreground">
          routes turns + visuals
        </div>
      ) : editable ? (
        <div className="mt-1.5 space-y-1">
          <input
            className="w-full rounded border border-input bg-background px-1.5 py-0.5 text-[11px]"
            value={d.name}
            placeholder="name"
            onChange={(e) => onPatch(d.path, { name: e.target.value })}
          />
          <select
            className="w-full rounded border border-input bg-background px-1 py-0.5 text-[11px]"
            value={d.model}
            onChange={(e) => onPatch(d.path, { model: e.target.value })}
          >
            <option value="">— model —</option>
            {models.map((m) => (
              <option key={m.id} value={m.id}>
                {m.label}
              </option>
            ))}
          </select>
          {d.kind === "discussant" ? (
            <input
              className="w-full rounded border border-input bg-background px-1.5 py-0.5 text-[11px]"
              value={d.aspect ?? ""}
              placeholder="aspect"
              onChange={(e) => onPatch(d.path, { aspect: e.target.value })}
            />
          ) : null}
        </div>
      ) : (
        <div className="mt-1 space-y-0.5">
          <div className="font-medium">{d.name}</div>
          {d.model ? (
            <div className="text-[10px] text-muted-foreground">{d.model}</div>
          ) : (
            <div className="text-[10px] text-destructive">no model</div>
          )}
          {d.aspect ? (
            <div className="text-[10px] italic text-muted-foreground">{d.aspect}</div>
          ) : null}
        </div>
      )}

      {d.tools.length > 0 ? (
        <div className="mt-1.5 flex flex-wrap gap-1">
          {d.tools.map((t) => (
            <Badge key={t}>{t}</Badge>
          ))}
        </div>
      ) : null}
      <Handle type="source" position={Position.Bottom} className="!bg-muted-foreground" />
    </div>
  );
}

export const nodeTypes = { agent: AgentFlowNode };
