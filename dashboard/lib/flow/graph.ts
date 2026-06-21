import type { Edge, Node } from "@xyflow/react";
import type { DiscussionScript } from "@/lib/schema/script-types";

// AgentNodeData is what every diagram node carries. activity is set only in
// the read-only live view (from the engine's agent_activity events).
export interface AgentNodeData {
  kind: "orchestrator" | "host" | "discussant" | "commander" | "viewer";
  /** Stable path into the script for edit mapping: "host", "discussant:0", … */
  path: string;
  name: string;
  model: string;
  aspect?: string;
  tools: string[];
  activity?: string;
  /** Tool name surfaced alongside an active state (e.g. "firecrawl"). */
  activityDetail?: string;
  [key: string]: unknown;
}

export type AgentNode = Node<AgentNodeData>;

// Tool profiles per role, mirroring what the engine assigns. The diagram shows
// these as badges; they aren't user-editable.
const HOST_TOOLS = ["take_note", "look_up_quote"];
const DISCUSSANT_TOOLS = ["take_note", "look_up_quote", "data_store", "firecrawl"];
const COMMANDER_TOOLS = ["image", "music"];

// scriptToFlow derives nodes + edges from the current script. Node positions
// are laid out deterministically; the editor preserves user drags separately.
export function scriptToFlow(script: DiscussionScript): {
  nodes: AgentNode[];
  edges: Edge[];
} {
  const nodes: AgentNode[] = [];
  const edges: Edge[] = [];

  nodes.push({
    id: "orchestrator",
    type: "agent",
    position: { x: 0, y: 0 },
    data: {
      kind: "orchestrator",
      path: "orchestrator",
      name: "Orchestrator",
      model: "",
      tools: [],
    },
  });

  nodes.push({
    id: "host",
    type: "agent",
    position: { x: -460, y: 200 },
    data: {
      kind: "host",
      path: "host",
      name: script.host?.name || "Host",
      model: script.host?.model || "",
      tools: HOST_TOOLS,
    },
  });
  edges.push({ id: "e-orch-host", source: "orchestrator", target: "host" });

  const count = script.discussants?.length ?? 0;
  script.discussants?.forEach((d, i) => {
    const id = `discussant:${i}`;
    const spread = (i - (count - 1) / 2) * 420;
    nodes.push({
      id,
      type: "agent",
      position: { x: spread, y: 400 },
      data: {
        kind: "discussant",
        path: id,
        name: d.name || `Discussant ${i + 1}`,
        model: d.model || "",
        aspect: d.aspect,
        tools: DISCUSSANT_TOOLS,
      },
    });
    edges.push({ id: `e-orch-${id}`, source: "orchestrator", target: id });
  });

  nodes.push({
    id: "commander",
    type: "agent",
    position: { x: 460, y: 200 },
    data: {
      kind: "commander",
      path: "commander",
      name: script.commander?.name || "Commander",
      model: script.commander?.model || "",
      tools: COMMANDER_TOOLS,
    },
  });
  edges.push({
    id: "e-orch-commander",
    source: "orchestrator",
    target: "commander",
  });

  script.viewers?.forEach((v, i) => {
    const id = `viewer:${i}`;
    nodes.push({
      id,
      type: "agent",
      position: { x: -460 - i * 200, y: 0 },
      data: {
        kind: "viewer",
        path: id,
        name: v.name || `Viewer ${i + 1}`,
        model: v.model || "",
        tools: [],
      },
    });
    edges.push({ id: `e-${id}-orch`, source: id, target: "orchestrator" });
  });

  return { nodes, edges };
}

// patchScript applies a node-level edit (from inline diagram editing) back into
// the script by its stable path. Returns a new script object.
export function patchScript(
  script: DiscussionScript,
  path: string,
  partial: { name?: string; model?: string; aspect?: string },
): DiscussionScript {
  const next: DiscussionScript = structuredClone(script);
  if (path === "host") Object.assign(next.host, partial);
  else if (path === "commander") Object.assign(next.commander, partial);
  else if (path.startsWith("discussant:")) {
    const i = Number(path.split(":")[1]);
    if (next.discussants[i]) Object.assign(next.discussants[i], partial);
  } else if (path.startsWith("viewer:")) {
    const i = Number(path.split(":")[1]);
    if (next.viewers?.[i]) Object.assign(next.viewers[i], partial);
  }
  return next;
}
