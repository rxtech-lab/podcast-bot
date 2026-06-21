"use server";

import { redirect } from "next/navigation";
import { auth } from "@/lib/auth";
import { improveScript, planScript, type PlanResponse } from "@/lib/engine";
import type { DiscussionScript } from "@/lib/schema/script-types";

async function requireSession() {
  const session = await auth();
  if (!session?.user?.id) redirect("/login");
  return session;
}

// generateScript drafts a fresh discussion script from a topic. The draft is
// held in the client until the user confirms (then createProject persists it).
export async function generateScript(input: {
  topic: string;
  discussants?: number;
  language?: string;
}): Promise<PlanResponse> {
  await requireSession();
  return planScript({
    type: "discussion",
    topic: input.topic,
    discussants: input.discussants,
    language: input.language,
    research: true,
  });
}

export async function reviseScript(input: {
  previousScript: DiscussionScript;
  instruction: string;
}): Promise<PlanResponse> {
  await requireSession();
  return improveScript(input);
}
