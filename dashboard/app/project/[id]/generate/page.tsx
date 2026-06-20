import { notFound, redirect } from "next/navigation";
import { getProject } from "@/lib/actions/projects";
import { loadJobLog } from "@/lib/actions/job-log";
import { getModels } from "@/lib/engine";
import { AppHeader } from "@/components/app-header";
import { VideoConfigClient } from "./video-config-client";
import { LiveClient } from "./live-client";

export default async function GeneratePage({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = await params;
  const project = await getProject(id);
  if (!project || !project.scriptJson) notFound();
  if (project.status === "planning") redirect("/project/new");

  // Before submission: show the video-config form.
  if (!project.engineJobId) {
    return (
      <div>
        <AppHeader />
        <VideoConfigClient projectId={id} />
      </div>
    );
  }

  let models: { id: string; label: string }[] = [];
  try {
    const res = await getModels();
    models = res.models.map((m) => ({ id: m.id, label: m.label }));
  } catch {
    models = [];
  }

  // Rehydrate the persisted feed so reloads (and finished jobs, which no longer
  // have a live SSE stream) still show the full transcript.
  const initialFeed = await loadJobLog(project.engineJobId).catch(() => []);

  return (
    <LiveClient
      projectId={id}
      jobId={project.engineJobId}
      script={project.scriptJson}
      models={models}
      initialDone={project.status === "done"}
      initialVideoUrl={project.videoUrl}
      initialFeed={initialFeed}
    />
  );
}
