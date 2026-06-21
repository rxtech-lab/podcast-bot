import { notFound, redirect } from "next/navigation";
import { getProject } from "@/lib/actions/projects";
import { getModels } from "@/lib/engine";
import { emptyDiscussionScript } from "@/lib/schema/script-types";
import { EditorClient } from "./editor-client";

export default async function EditorPage({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = await params;
  const project = await getProject(id);
  if (!project) notFound();
  if (project.status === "generating" || project.status === "done") {
    redirect(`/project/${id}`);
  }

  // Models drive the per-agent pickers; degrade to an empty list if the engine
  // is unreachable so the editor still renders.
  let models: { id: string; label: string }[] = [];
  try {
    const res = await getModels();
    models = res.models.map((m) => ({ id: m.id, label: m.label }));
  } catch {
    models = [];
  }

  return (
    <EditorClient
      projectId={id}
      initialScript={project.scriptJson ?? emptyDiscussionScript()}
      models={models}
    />
  );
}
