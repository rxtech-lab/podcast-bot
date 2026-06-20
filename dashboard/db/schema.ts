import { sql } from "drizzle-orm";
import { index, integer, sqliteTable, text } from "drizzle-orm/sqlite-core";
import type { DiscussionScript, VideoConfig } from "@/lib/schema/script-types";
import type { FeedItem } from "@/lib/feed";

// project is a single debate/game script owned by a signed-in user. It moves
// through four states:
//   planning   — draft generated, not yet confirmed/persisted as editable
//   setup      — confirmed; user is editing the form + diagram (autosaved)
//   generating — submitted to the engine; a video job is running
//   done       — the engine finished; videoUrl points at the download
export type ProjectStatus = "planning" | "setup" | "generating" | "done";

export const projects = sqliteTable(
  "project",
  {
    id: text("id")
      .primaryKey()
      .$defaultFn(() => crypto.randomUUID()),
    userId: text("user_id").notNull(),
    title: text("title").notNull(),
    type: text("type").notNull().default("discussion"),
    status: text("status").$type<ProjectStatus>().notNull().default("planning"),
    topic: text("topic"),
    scriptJson: text("script_json", { mode: "json" }).$type<DiscussionScript>(),
    videoConfig: text("video_config", { mode: "json" }).$type<VideoConfig>(),
    engineJobId: text("engine_job_id"),
    videoUrl: text("video_url"),
    createdAt: integer("created_at", { mode: "timestamp_ms" })
      .notNull()
      .default(sql`(unixepoch() * 1000)`),
    updatedAt: integer("updated_at", { mode: "timestamp_ms" })
      .notNull()
      .default(sql`(unixepoch() * 1000)`),
  },
  (t) => ({
    byUser: index("project_user_id_idx").on(t.userId),
  }),
);

export type Project = typeof projects.$inferSelect;
export type NewProject = typeof projects.$inferInsert;

// jobLog persists the live generation feed (transcript bubbles + system notes)
// for one engine job, so the log survives reloads and is replayable after the
// job finishes — the engine's SSE stream has no backlog, so without this a
// completed job shows an empty log. Stored as one JSON blob keyed by job id;
// the in-memory feed is already a capped, ordered list, so last-write-wins is
// sufficient and avoids per-event row churn.
export const jobLogs = sqliteTable("job_log", {
  jobId: text("job_id").primaryKey(),
  projectId: text("project_id").notNull(),
  userId: text("user_id").notNull(),
  feedJson: text("feed_json", { mode: "json" })
    .$type<FeedItem[]>()
    .notNull()
    .default(sql`'[]'`),
  updatedAt: integer("updated_at", { mode: "timestamp_ms" })
    .notNull()
    .default(sql`(unixepoch() * 1000)`),
});

export type JobLog = typeof jobLogs.$inferSelect;
