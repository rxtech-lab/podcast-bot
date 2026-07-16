import Foundation

/// Lifecycle of a server-owned discussion.
enum DiscussionStatus: String, Codable, Sendable {
    case planning
    case generating
    case ready
    case failed
}

enum DiscussionVisibility: String, Codable, Sendable {
    case `private`
    case `public`
}

struct DiscussionCover: Codable, Hashable, Sendable {
    var type: String?
    var imageURL: String?
    var imageKey: String?
    var gradientStart: String?
    var gradientEnd: String?
    var prompt: String?

    enum CodingKeys: String, CodingKey {
        case type
        case imageURL = "image_url"
        case imageKey = "image_key"
        case gradientStart = "gradient_start"
        case gradientEnd = "gradient_end"
        case prompt
    }

    var hasImage: Bool {
        let url = imageURL?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        let key = imageKey?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return !url.isEmpty || !key.isEmpty
    }

    var renderableImageURL: URL? {
        guard let urlString = imageURL?.trimmingCharacters(in: .whitespacesAndNewlines),
              !urlString.isEmpty else { return nil }
        return URL(string: urlString)
    }

    var hasGradient: Bool {
        let start = gradientStart?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        let end = gradientEnd?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return !start.isEmpty && !end.isEmpty
    }

    var isPublishable: Bool {
        switch type {
        case "image", "ai":
            return hasImage
        case "gradient":
            return hasGradient
        default:
            return false
        }
    }
}

struct CreatorProfile: Identifiable, Codable, Hashable, Sendable {
    var id: String
    var displayName: String
    var username: String?
    var avatarURL: String?
    var followerCount: Int?
    var isFollowed: Bool?
    var isSelf: Bool?

    enum CodingKeys: String, CodingKey {
        case id
        case displayName = "display_name"
        case username
        case avatarURL = "avatar_url"
        case followerCount = "follower_count"
        case isFollowed = "is_followed"
        case isSelf = "is_self"
    }

    var title: String {
        let name = displayName.trimmingCharacters(in: .whitespacesAndNewlines)
        return name.isEmpty ? String(localized: "Creator", comment: "Fallback creator name") : name
    }

    var subtitle: String {
        if let username, !username.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            return "@\(username)"
        }
        return followerText
    }

    var followerText: String {
        let count = followerCount ?? 0
        return count == 1
            ? String(localized: "1 follower", comment: "Singular creator follower count")
            : String(localized: "\(count) followers", comment: "Plural creator follower count")
    }
}

struct MarketProfile: Codable, Hashable, Sendable {
    var profile: CreatorProfile
    var stations: [Discussion]
    var following: [CreatorProfile]
}

enum DiscussionTranslationStatus: String, Codable, Hashable, Sendable {
    case generating
    case ready
    case failed
}

struct DiscussionTranslationMeta: Codable, Hashable, Sendable, Identifiable {
    var language: String
    var status: DiscussionTranslationStatus
    var available: Bool
    var pending: Bool?
    var error: String?
    var generatedAt: String?
    /// Language-dedicated cover art; nil means viewers of this language fall
    /// back to the podcast's default cover.
    var cover: DiscussionCover?

    var id: String { language }

    enum CodingKeys: String, CodingKey {
        case language, status, available, pending, error, cover
        case generatedAt = "generated_at"
    }
}

/// A planned + generated audio discussion. Durable storage now lives on the
/// engine side; the app keeps only this in-memory snapshot from the API.
struct Discussion: Identifiable, Codable, Hashable, Sendable {
    var id: String
    var topic: String
    var title: String
    var status: DiscussionStatus
    var language: String
    /// The source language remains stable when this detail is presented through
    /// a translated bundle. A nil value means this is an older/list payload.
    var mainLanguage: String? = nil
    var translations: [DiscussionTranslationMeta]? = nil
    var jobID: String?
    var downloadURLString: String?
    var durationSeconds: Double?
    var promptTokens: Int?
    var completionTokens: Int?
    var totalTokens: Int?
    var llmCostUSD: Double?
    var llmCostKnown: Bool?
    var ttsCostUSD: Double?
    var musicCostUSD: Double?
    /// Total points charged across this podcast's lifecycle (planning +
    /// generation). The only usage figure shown to users; the token/cost fields
    /// above are zeroed by the server once the points economy is enabled.
    var pointsCharged: Int?
    var showUsageSummary: Bool?
    var visibility: DiscussionVisibility?
    /// Server-built canonical public web link (`/p/{id}`) for sharing — the same
    /// URL embedded as the summary Markdown's "listen again" link. The client
    /// never constructs share links itself; it shares this verbatim.
    var shareURL: String?
    var cover: DiscussionCover?
    var creator: CreatorProfile?
    var likeCount: Int?
    var isLiked: Bool?
    var isOwner: Bool?
    var publishedAt: String?
    var script: ScriptDTO?
    var markdown: String?
    var sources: [SourceDTO]?
    var researched: Bool?
    var referenceDiscussionID: String?
    /// Album this podcast belongs to (audiobook chapter batches and follow-ups
    /// are auto-bundled; users can also group podcasts manually). `album` is
    /// the compact summary attached to list rows for home-screen grouping.
    var albumID: String?
    var album: AlbumSummaryDTO?
    var lines: [DiscussionLineDTO]?
    var editTurns: [DiscussionEditTurnDTO]?
    var editTurnsHasMore: Bool?
    var editTurnsBefore: Int64?
    var progress: DiscussionProgressDTO?
    /// Content-free descriptor of the podcast's generated summary document. nil
    /// when no summary exists yet (e.g. the podcast hasn't finished). The Markdown
    /// body is never carried here — it is fetched separately when the summary view
    /// mounts.
    var summary: SummaryMeta?
    /// Content-free descriptor of the discussion's generated mindmap. Present
    /// only for discussion-type podcasts; the node tree body is fetched
    /// separately when the mindmap view mounts.
    var mindmap: SummaryMeta?
    var allowSendingMessage: Bool?
    var createdAt: String?
    var updatedAt: String?

    enum CodingKeys: String, CodingKey {
        case id
        case topic
        case title
        case status
        case language
        case mainLanguage = "main_language"
        case translations
        case jobID = "job_id"
        case downloadURLString = "download_url"
        case durationSeconds = "duration_seconds"
        case promptTokens = "prompt_tokens"
        case completionTokens = "completion_tokens"
        case totalTokens = "total_tokens"
        case llmCostUSD = "llm_cost_usd"
        case llmCostKnown = "llm_cost_known"
        case ttsCostUSD = "tts_cost_usd"
        case musicCostUSD = "music_cost_usd"
        case pointsCharged = "points_charged"
        case showUsageSummary
        case visibility
        case shareURL = "share_url"
        case cover
        case creator
        case likeCount = "like_count"
        case isLiked = "is_liked"
        case isOwner = "is_owner"
        case publishedAt = "published_at"
        case script
        case markdown
        case sources
        case researched
        case referenceDiscussionID = "reference_discussion_id"
        case albumID = "album_id"
        case album
        case lines
        case editTurns = "edit_turns"
        case editTurnsHasMore = "edit_turns_has_more"
        case editTurnsBefore = "edit_turns_before"
        case progress
        case summary
        case mindmap
        case allowSendingMessage
        case createdAt = "created_at"
        case updatedAt = "updated_at"
    }

    var displayTitle: String {
        if !title.isEmpty { return title }
        if let scriptTitle = script?.title, !scriptTitle.isEmpty { return scriptTitle }
        return topic
    }

    var isPublic: Bool { visibility == .public }

    func preservingRenderableCover(from existing: Discussion) -> Discussion {
        guard id == existing.id,
              cover?.renderableImageURL == nil,
              let existingCover = existing.cover,
              existingCover.renderableImageURL != nil else { return self }
        var copy = self
        if var cover = copy.cover {
            cover.imageURL = existingCover.imageURL
            copy.cover = cover
        } else {
            copy.cover = existingCover
        }
        return copy
    }

    var canSendMessages: Bool {
        status == .generating && (allowSendingMessage ?? true)
    }

    var sortedPeople: [PlanPersonSnapshot] {
        var people: [PlanPersonSnapshot] = []
        if script?.type == "audio-book" {
            var seenNames = Set<String>()
            if let host = script?.audioBookHost, !host.name.isEmpty {
                people.append(PlanPersonSnapshot(name: host.name, aspect: "Narrator", isHost: true))
                seenNames.insert(Self.normalizedPersonName(host.name))
            }
            for speaker in script?.audioBookSpeakers ?? [] {
                let key = Self.normalizedPersonName(speaker.name)
                guard !key.isEmpty, !seenNames.contains(key) else { continue }
                people.append(PlanPersonSnapshot(name: speaker.name, aspect: speaker.description ?? speaker.gender ?? "", isHost: false))
                seenNames.insert(key)
            }
            return people
        }
        if let host = script?.host, !host.name.isEmpty {
            people.append(PlanPersonSnapshot(name: host.name, aspect: "Moderator", isHost: true))
        }
        people.append(contentsOf: (script?.discussants ?? []).map {
            PlanPersonSnapshot(name: $0.name, aspect: $0.aspect ?? "", isHost: false)
        })
        return people
    }

    private static func normalizedPersonName(_ name: String) -> String {
        name.trimmingCharacters(in: .whitespacesAndNewlines)
            .lowercased()
    }

    var sortedSources: [PlanSourceSnapshot] {
        (sources ?? script?.sources ?? []).map {
            PlanSourceSnapshot(
                title: $0.title,
                urlString: $0.url,
                snippet: $0.snippet ?? "",
                markdown: $0.markdown ?? ""
            )
        }
    }

    var sortedLines: [DiscussionLineDTO] { lines ?? [] }

    /// User-facing points label for a finished/known podcast, e.g. "812 points".
    /// nil until generation has finished and any points have been charged.
    var pointsText: String? {
        guard showUsageSummary == true else { return nil }
        guard status == .ready else { return nil }
        guard let pts = pointsCharged, pts > 0 else { return nil }
        let formatted = UsageSummary.formatInt(pts)
        return "\(formatted) point\(pts == 1 ? "" : "s")"
    }

    /// Whether a summary document is ready to view for this podcast. True only
    /// once the server has generated it (status `ready`).
    var hasSummary: Bool { summary?.available == true }

    /// Whether summary generation is already in progress.
    var summaryPending: Bool { summary?.pending == true || summary?.status == .generating }

    /// Whether the backend says this owner can manually trigger summary
    /// generation for a ready podcast that has no finished summary.
    var canGenerateSummary: Bool { summary?.generation == true }

    /// Whether a mindmap is ready to view for this discussion podcast.
    var hasMindmap: Bool { mindmap?.available == true }

    /// Whether mindmap generation is already in progress.
    var mindmapPending: Bool { mindmap?.pending == true || mindmap?.status == .generating }

    /// Whether the backend says this owner can manually trigger mindmap
    /// generation for a ready discussion that has no finished mindmap.
    var canGenerateMindmap: Bool { mindmap?.generation == true }
}

/// Lifecycle of a generated summary document.
enum SummaryStatus: String, Codable, Hashable, Sendable {
    case generating
    case ready
    case failed
}

/// Content-free descriptor of a podcast's summary document, returned on the
/// discussion detail payload. The Markdown body is fetched separately.
struct SummaryMeta: Codable, Hashable, Sendable {
    var docType: String?
    var status: SummaryStatus?
    var available: Bool?
    var pending: Bool?
    var generation: Bool?
    var generatedAt: String?

    enum CodingKeys: String, CodingKey {
        case docType = "doc_type"
        case status
        case available
        case pending
        case generation
        case generatedAt = "generated_at"
    }
}

/// Full summary payload (including the Markdown body) returned by the summary
/// content endpoint, fetched only when the summary view mounts.
struct SummaryDocument: Codable, Hashable, Sendable {
    var docType: String?
    var status: SummaryStatus?
    var markdown: String
    var generatedAt: String?

    enum CodingKeys: String, CodingKey {
        case docType = "doc_type"
        case status
        case markdown
        case generatedAt = "generated_at"
    }
}

struct DiscussionProgressDTO: Codable, Hashable, Sendable {
    var active: Bool
    var operation: String?
    var phase: String?
    var text: String?
    var updatedAt: String?

    enum CodingKeys: String, CodingKey {
        case active
        case operation
        case phase
        case text
        case updatedAt = "updated_at"
    }
}

/// One turn in the plan-editing chat, persisted server-side so the history
/// survives app restarts. `role` is "user" (an instruction the user sent, or an
/// "added sources" action) or "plan" (a plan revision). Plan turns carry a full
/// snapshot of the plan at that moment so each card can be rebuilt.
struct DiscussionEditTurnDTO: Codable, Hashable, Sendable {
    var id: Int64?
    var role: String
    var text: String?
    var script: ScriptDTO?
    var sources: [SourceDTO]?
    var markdown: String?
    var createdAt: String?

    enum CodingKeys: String, CodingKey {
        case id
        case role
        case text
        case script
        case sources
        case markdown
        case createdAt = "created_at"
    }
}

struct DiscussionLineDTO: Codable, Hashable, Sendable {
    var speaker: String
    var role: String
    var side: String?
    var text: String
    var startMS: Int?
    var isUser: Bool
    /// Server-owned id of the human who sent this line, used to tell *my* messages
    /// apart from other participants' (both arrive with `isUser == true`). Nil for
    /// agent lines and for legacy rows persisted before the column existed.
    var senderUserID: String? = nil
    /// Ephemeral playback URL for a voice message, re-signed by the server on each
    /// read. Nil for normal text lines.
    var audioURL: String? = nil
    /// Inline audiobook illustration URL. Image-only rows have empty text but
    /// still render as their own transcript bubble.
    var imageURL: String? = nil
    var sources: [SourceDTO]? = nil
    var judgementComment: String? = nil

    enum CodingKeys: String, CodingKey {
        case speaker
        case role
        case side
        case text
        case startMS = "start_ms"
        case isUser = "is_user"
        case senderUserID = "sender_user_id"
        case audioURL = "audio_url"
        case imageURL = "image_url"
        case sources
        case judgementComment = "judgement_comment"
    }
}
