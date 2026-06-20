// These types mirror the engine's config.DebateTopic (discussion subset) and
// its JSON tags exactly, so a script round-trips through POST /api/jobs/json
// and the planning endpoints without translation.

export interface AgentSpec {
  name: string;
  model: string;
  base_url?: string;
  api_key?: string;
  /** The angle a discussant argues from (e.g. "economic", "ethical"). */
  aspect?: string;
}

export interface DiscussionScript {
  title: string;
  type: "discussion";
  language: string;
  channel: string;
  total_minutes: number;
  segment_max_seconds: number;
  tts_provider?: "azure" | "eleven";
  resolution?: "720p" | "1080p" | "4k";
  storage?: "plaintext" | "mongodb";
  host: AgentSpec;
  discussants: AgentSpec[];
  commander: AgentSpec;
  viewers?: AgentSpec[];
  /** Markdown body rendered as the `## Background` section. */
  background?: string;
}

export interface VideoConfig {
  soft_subs: boolean;
  burn_subs: boolean;
  subtitle_languages: string[];
  resolution: "720p" | "1080p";
}

/** A blank, valid-enough discussion script used as an editor fallback. */
export function emptyDiscussionScript(): DiscussionScript {
  return {
    title: "Untitled discussion",
    type: "discussion",
    language: "en-US",
    channel: "default",
    total_minutes: 30,
    segment_max_seconds: 60,
    tts_provider: "azure",
    resolution: "1080p",
    storage: "plaintext",
    host: { name: "Host", model: "" },
    discussants: [
      { name: "Discussant A", model: "", aspect: "" },
      { name: "Discussant B", model: "", aspect: "" },
    ],
    commander: { name: "Commander", model: "" },
    background: "",
  };
}

export const defaultVideoConfig: VideoConfig = {
  soft_subs: false,
  burn_subs: false,
  subtitle_languages: [],
  resolution: "1080p",
};
