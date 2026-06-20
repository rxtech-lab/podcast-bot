"use server";

import { and, desc, eq } from "drizzle-orm";
import { redirect } from "next/navigation";
import { revalidatePath } from "next/cache";
import { auth } from "@/lib/auth";
import { db, projects, type Project, type ProjectStatus } from "@/db";
import type { DiscussionScript, VideoConfig } from "@/lib/schema/script-types";

async function requireUserId(): Promise<string> {
  const session = await auth();
  if (!session?.user?.id) redirect("/login");
  return session.user.id;
}

export async function listProjects(): Promise<Project[]> {
  const userId = await requireUserId();
  return db
    .select()
    .from(projects)
    .where(eq(projects.userId, userId))
    .orderBy(desc(projects.updatedAt));
}

export async function getProject(id: string): Promise<Project | null> {
  const userId = await requireUserId();
  const rows = await db
    .select()
    .from(projects)
    .where(and(eq(projects.id, id), eq(projects.userId, userId)))
    .limit(1);
  return rows[0] ?? null;
}

export async function createProject(input: {
  title: string;
  topic: string;
  scriptJson: DiscussionScript;
}): Promise<Project> {
  const userId = await requireUserId();
  const [row] = await db
    .insert(projects)
    .values({
      userId,
      title: input.title,
      type: "discussion",
      status: "setup",
      topic: input.topic,
      scriptJson: input.scriptJson,
    })
    .returning();
  revalidatePath("/projects");
  return row;
}

// updateScript is the silent autosave path — no revalidatePath so the editor
// isn't re-rendered out from under the user while they type.
export async function updateScript(
  id: string,
  scriptJson: DiscussionScript,
): Promise<void> {
  const userId = await requireUserId();
  await db
    .update(projects)
    .set({
      scriptJson,
      title: scriptJson.title || "Untitled discussion",
      updatedAt: new Date(),
    })
    .where(and(eq(projects.id, id), eq(projects.userId, userId)));
}

export async function setVideoConfig(
  id: string,
  videoConfig: VideoConfig,
): Promise<void> {
  const userId = await requireUserId();
  await db
    .update(projects)
    .set({ videoConfig, updatedAt: new Date() })
    .where(and(eq(projects.id, id), eq(projects.userId, userId)));
}

export async function setStatus(
  id: string,
  status: ProjectStatus,
  extra: { engineJobId?: string; videoUrl?: string } = {},
): Promise<void> {
  const userId = await requireUserId();
  await db
    .update(projects)
    .set({ status, ...extra, updatedAt: new Date() })
    .where(and(eq(projects.id, id), eq(projects.userId, userId)));
  revalidatePath("/projects");
}

export async function deleteProject(id: string): Promise<void> {
  const userId = await requireUserId();
  await db
    .delete(projects)
    .where(and(eq(projects.id, id), eq(projects.userId, userId)));
  revalidatePath("/projects");
}
