"use client";

import { useCallback, useMemo, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import Form from "@rjsf/core";
import validator from "@rjsf/validator-ajv8";
import type { IChangeEvent } from "@rjsf/core";
import { useDebouncedCallback } from "use-debounce";
import Link from "next/link";
import { Button } from "@/components/ui/button";
import { ScriptDiagram } from "@/components/script-diagram";
import {
  discussionUiSchema,
  withModelEnum,
} from "@/lib/schema/discussion.schema";
import { patchScript } from "@/lib/flow/graph";
import { updateScript } from "@/lib/actions/projects";
import type { DiscussionScript } from "@/lib/schema/script-types";

type SaveState = "idle" | "saving" | "saved";

export function EditorClient({
  projectId,
  initialScript,
  models,
}: {
  projectId: string;
  initialScript: DiscussionScript;
  models: { id: string; label: string }[];
}) {
  const router = useRouter();
  const [script, setScript] = useState<DiscussionScript>(initialScript);
  const [save, setSave] = useState<SaveState>("idle");
  const latest = useRef(script);

  const schema = useMemo(() => withModelEnum(models.map((m) => m.id)), [models]);

  const persist = useDebouncedCallback(async (next: DiscussionScript) => {
    setSave("saving");
    try {
      await updateScript(projectId, next);
      setSave("saved");
    } catch {
      setSave("idle");
    }
  }, 800);

  // Single mutation path for both panels: update state + schedule autosave.
  const apply = useCallback(
    (next: DiscussionScript) => {
      latest.current = next;
      setScript(next);
      persist(next);
    },
    [persist],
  );

  const onFormChange = (e: IChangeEvent<DiscussionScript>) => {
    if (e.formData) apply(e.formData);
  };

  const onPatch = useCallback(
    (path: string, partial: { name?: string; model?: string; aspect?: string }) => {
      apply(patchScript(latest.current, path, partial));
    },
    [apply],
  );

  return (
    <div className="flex h-screen flex-col">
      <header className="flex items-center justify-between border-b border-border px-6 py-3">
        <div className="flex items-center gap-3">
          <Link href="/projects" className="text-sm text-muted-foreground">
            ← Projects
          </Link>
          <span className="font-semibold">{script.title || "Untitled"}</span>
          <span className="text-xs text-muted-foreground">
            {save === "saving" ? "Saving…" : save === "saved" ? "Saved" : ""}
          </span>
        </div>
        <Button onClick={() => router.push(`/project/${projectId}/generate`)}>
          Generate video
        </Button>
      </header>

      <div className="grid flex-1 grid-cols-1 overflow-hidden lg:grid-cols-2">
        <div className="overflow-y-auto border-r border-border p-5">
          <div className="rjsf">
            <Form
              schema={schema}
              uiSchema={discussionUiSchema}
              formData={script}
              validator={validator}
              onChange={onFormChange}
              liveValidate={false}
            />
          </div>
        </div>
        <div className="h-[50vh] lg:h-auto">
          <ScriptDiagram script={script} models={models} onPatch={onPatch} />
        </div>
      </div>
    </div>
  );
}
