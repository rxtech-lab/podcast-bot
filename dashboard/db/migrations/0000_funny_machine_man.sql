CREATE TABLE `project` (
	`id` text PRIMARY KEY NOT NULL,
	`user_id` text NOT NULL,
	`title` text NOT NULL,
	`type` text DEFAULT 'discussion' NOT NULL,
	`status` text DEFAULT 'planning' NOT NULL,
	`topic` text,
	`script_json` text,
	`video_config` text,
	`engine_job_id` text,
	`video_url` text,
	`created_at` integer DEFAULT (unixepoch() * 1000) NOT NULL,
	`updated_at` integer DEFAULT (unixepoch() * 1000) NOT NULL
);
--> statement-breakpoint
CREATE INDEX `project_user_id_idx` ON `project` (`user_id`);