"use client";

import { createContext, useContext } from "react";

export interface NodeEditContext {
  readOnly: boolean;
  models: { id: string; label: string }[];
  onPatch: (
    path: string,
    partial: { name?: string; model?: string; aspect?: string },
  ) => void;
}

const Ctx = createContext<NodeEditContext>({
  readOnly: true,
  models: [],
  onPatch: () => {},
});

export const NodeEditProvider = Ctx.Provider;
export const useNodeEdit = () => useContext(Ctx);
