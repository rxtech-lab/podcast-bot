"use server";

import { redirect } from "next/navigation";
import { auth } from "@/lib/auth";
import { getJob, sendParticipantMessage, submitJobJSON } from "@/lib/engine";
import { getProject, setStatus } from "@/lib/actions/projects";
import { defaultVideoConfig, type VideoConfig } from "@/lib/schema/script-types";

async function requireUserId() {
  const session = await auth();
  if (!session?.user?.id) redirect("/login");
  return session.user.id;
}

// submitGeneration renders the project's script to the engine as a video job
// and flips the project into the "generating" state, returning the engine job
// id so the live view can subscribe.
export async function submitGeneration(
  projectId: string,
  videoConfig: VideoConfig,
): Promise<{ jobId: string }> {
  await requireUserId();
  const project = await getProject(projectId);
  if (!project?.scriptJson) throw new Error("project has no script");

  const { id } = await submitJobJSON({
    script: project.scriptJson,
    videoConfig: videoConfig ?? defaultVideoConfig,
  });
  await setStatus(projectId, "generating", { engineJobId: id });
  return { jobId: id };
}

// pollJob is called by the live view to detect completion and persist the
// final download link. Returns the engine job snapshot.
export async function pollJob(projectId: string, jobId: string) {
  await requireUserId();
  const job = await getJob(jobId);
  if (job.status === "done") {
    // Persist the STABLE dashboard proxy route — never the engine's
    // download_url, which is a presigned S3 link that expires in an hour.
    // The proxy mints a fresh presigned redirect on each request.
    await setStatus(projectId, "done", {
      videoUrl: `/api/engine/jobs/${jobId}/video`,
    });
  }
  return job;
}

export async function participate(jobId: string, text: string): Promise<void> {
  await requireUserId();
  await sendParticipantMessage(jobId, text);
}
