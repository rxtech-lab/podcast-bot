"use client";

import { useEffect, useMemo, useRef } from "react";
import {
  Background,
  Controls,
  ReactFlow,
  useEdgesState,
  useNodesState,
  type Node,
} from "@xyflow/react";
import { scriptToFlow, type AgentNodeData } from "@/lib/flow/graph";
import { nodeTypes } from "@/lib/flow/node-types";
import { NodeEditProvider } from "@/lib/flow/node-context";
import type { DiscussionScript } from "@/lib/schema/script-types";

export interface ScriptDiagramProps {
  script: DiscussionScript;
  models: { id: string; label: string }[];
  readOnly?: boolean;
  onPatch?: (
    path: string,
    partial: { name?: string; model?: string; aspect?: string },
  ) => void;
  /** Live activity per node path (read-only view): visual state + tool detail. */
  activity?: Record<string, { state: string; detail?: string }>;
}

export function ScriptDiagram({
  script,
  models,
  readOnly = false,
  onPatch = () => {},
  activity,
}: ScriptDiagramProps) {
  const derived = useMemo(() => scriptToFlow(script), [script]);
  const [nodes, setNodes, onNodesChange] = useNodesState(derived.nodes);
  const [edges, setEdges, onEdgesChange] = useEdgesState(derived.edges);

  // Preserve user-dragged positions across re-derivations keyed by node id.
  const positions = useRef<Record<string, { x: number; y: number }>>({});

  useEffect(() => {
    const next = derived.nodes.map((n) => {
      const pos = positions.current[n.id] ?? n.position;
      const act = activity?.[n.data.path];
      return {
        ...n,
        position: pos,
        data: { ...n.data, activity: act?.state, activityDetail: act?.detail },
      } as Node<AgentNodeData>;
    });
    setNodes(next);
    setEdges(derived.edges);
  }, [derived, activity, setNodes, setEdges]);

  const ctx = useMemo(
    () => ({
      readOnly,
      models,
      onPatch,
    }),
    [readOnly, models, onPatch],
  );

  return (
    <div className="h-full w-full">
      <NodeEditProvider value={ctx}>
        <ReactFlow
          nodes={nodes}
          edges={edges}
          nodeTypes={nodeTypes}
          onNodesChange={onNodesChange}
          onEdgesChange={onEdgesChange}
          onNodeDragStop={(_, node) => {
            positions.current[node.id] = node.position;
          }}
          nodesDraggable={!readOnly}
          nodesConnectable={false}
          elementsSelectable={!readOnly}
          fitView
          proOptions={{ hideAttribution: true }}
        >
          <Background />
          <Controls showInteractive={false} />
        </ReactFlow>
      </NodeEditProvider>
    </div>
  );
}
