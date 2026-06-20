"use server";

import { and, eq } from "drizzle-orm";
import { redirect } from "next/navigation";
import { auth } from "@/lib/auth";
import { db, jobLogs, projects } from "@/db";
import type { FeedItem } from "@/lib/feed";

async function requireUserId(): Promise<string> {
  const session = await auth();
  if (!session?.user?.id) redirect("/login");
  return session.user.id;
}

// loadJobLog returns the persisted live feed for a job, or [] if none. Scoped
// to the signed-in user so one user can't read another's transcript.
export async function loadJobLog(jobId: string): Promise<FeedItem[]> {
  const userId = await requireUserId();
  const rows = await db
    .select({ feedJson: jobLogs.feedJson })
    .from(jobLogs)
    .where(and(eq(jobLogs.jobId, jobId), eq(jobLogs.userId, userId)))
    .limit(1);
  return rows[0]?.feedJson ?? [];
}

// saveJobLog upserts the whole feed blob for a job. The client debounces calls
// so this writes a few times per generation rather than per event. Ownership is
// verified against the project the job belongs to.
export async function saveJobLog(
  projectId: string,
  jobId: string,
  feed: FeedItem[],
): Promise<void> {
  const userId = await requireUserId();
  // Confirm the project (and thus the job) belongs to this user.
  const owned = await db
    .select({ id: projects.id })
    .from(projects)
    .where(and(eq(projects.id, projectId), eq(projects.userId, userId)))
    .limit(1);
  if (!owned[0]) return;

  await db
    .insert(jobLogs)
    .values({ jobId, projectId, userId, feedJson: feed, updatedAt: new Date() })
    .onConflictDoUpdate({
      target: jobLogs.jobId,
      set: { feedJson: feed, updatedAt: new Date() },
    });
}
