// FeedItem is one entry in the live generation feed: either a spoken message
// (rendered as a chat bubble) or a system note (phase/status/error). Kept in
// its own module (no "use client") so both the client hook and the Drizzle
// schema can share the type.
export interface FeedItem {
  type: "message" | "system";
  /** message: who spoke and their role; system: undefined. */
  speaker?: string;
  role?: string;
  /** system level: "status" | "phase" | "error". */
  level?: string;
  text: string;
}
