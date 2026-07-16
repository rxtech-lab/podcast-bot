import Foundation
import Observation
import AVFoundation
import MediaPlayer
import SwiftUI
import UIKit
import os

let playerLog = Logger(subsystem: "com.debatebot.ios", category: "PlayerModel")

/// One transcript bubble shown in the player. While an utterance streams it is
/// updated in place; when `done` it is finalized (and persisted).
struct LiveLine: Identifiable, Equatable {
    let id = UUID()
    var speaker: String
    var role: String
    var text: String
    var isUser: Bool
    var done: Bool
    /// Server-owned id of the human who authored this line. Used to distinguish the
    /// current user's own messages from other participants' (both are `isUser`).
    /// Nil for agent lines and legacy rows without a persisted sender.
    var senderUserID: String? = nil
    /// Playback URL when this line is a voice message; nil for text-only lines.
    var audioURL: String? = nil
    /// Illustration URL when this line is an image bubble (audiobook content);
    /// nil for ordinary lines. The line's text may carry the artwork caption.
    var imageURL: String? = nil
    /// Audio-timeline position (seconds) of an audiobook illustration line,
    /// used to switch the player artwork in sync with playback. Nil when the
    /// server didn't record timing (legacy runs, non-image lines).
    var audioOffsetSeconds: Double? = nil
    var sources: [SourceDTO]? = nil
    var judgementComment: String? = nil

    var hasAudio: Bool {
        !(audioURL?.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ?? true)
    }

    var hasImage: Bool {
        !(imageURL?.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ?? true)
    }

    var displayText: String {
        Self.displayText(from: text)
    }

    var hasDisplayText: Bool {
        !displayText.isEmpty
    }

    var hasRenderablePayload: Bool {
        hasDisplayText ||
            hasAudio ||
            hasImage ||
            !(sources?.isEmpty ?? true) ||
            !(judgementComment?.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ?? true)
    }

    static func displayText(from raw: String) -> String {
        raw.replacingOccurrences(
            of: #"(?i)<\s*(?:pause\b[^>]*|breath\b[^>]*)/?\s*>|\[\s*(?:pause\b[^\]]*|breath\b[^\]]*)\]"#,
            with: " ",
            options: .regularExpression
        )
        .trimmingCharacters(in: .whitespacesAndNewlines)
    }

    @discardableResult
    static func applyTranscriptEvent(to lines: inout [LiveLine],
                                     speaker: String,
                                     role: String,
                                     text: String,
                                     done: Bool,
                                     sources: [SourceDTO]? = nil,
                                     judgementComment: String? = nil) -> LiveLine? {
        let chunk = text.trimmingCharacters(in: .whitespacesAndNewlines)
        // Only continue the *current* (last) streaming bubble, and only when it
        // belongs to the same speaker. Any intervening line — a different
        // speaker's turn, a user message, or an inline image — makes the last
        // line no longer a match, so this chunk starts a fresh bubble. That is
        // what breaks a long turn into per-speaker messages and splits text
        // around images, instead of appending forever to one growing bubble.
        if let idx = lines.indices.last,
           lines[idx].speaker == speaker,
           !lines[idx].done,
           !lines[idx].isUser,
           !lines[idx].hasImage {
            if !chunk.isEmpty {
                if lines[idx].text.isEmpty {
                    lines[idx].text = chunk
                } else {
                    lines[idx].text += " " + chunk
                }
            }
            if let sources {
                lines[idx].sources = sources
            }
            if let judgementComment {
                lines[idx].judgementComment = judgementComment
            }
            if done {
                lines[idx].done = true
                return lines[idx]
            }
            return nil
        }

        guard !chunk.isEmpty else { return nil }
        // Starting a new bubble (speaker changed, or the previous one was closed
        // by a user message / image): finish any still-open agent bubble so it
        // stops streaming and renders as a completed message.
        if let prev = lines.indices.last, !lines[prev].done, !lines[prev].isUser, !lines[prev].hasImage {
            lines[prev].done = true
        }
        let line = LiveLine(speaker: speaker, role: role, text: chunk, isUser: false, done: done,
                            sources: sources, judgementComment: judgementComment)
        lines.append(line)
        return done ? line : nil
    }

    /// Marks the current (last) streaming agent bubble finished, if there is one.
    /// Called before inserting a user message or an inline image so the text that
    /// streamed before it renders as its own completed bubble.
    static func finalizeLastOpenAgentLine(in lines: inout [LiveLine]) {
        guard let idx = lines.indices.last, !lines[idx].done, !lines[idx].isUser, !lines[idx].hasImage else { return }
        lines[idx].done = true
    }
}

struct TranscriptScrollToken: Equatable {
    var count: Int
    var lastID: UUID?
    var lastText: String
    var lastDone: Bool

    static func make(for lines: [LiveLine]) -> TranscriptScrollToken {
        guard let last = lines.last else {
            return TranscriptScrollToken(count: 0, lastID: nil, lastText: "", lastDone: false)
        }
        return TranscriptScrollToken(count: lines.count,
                                     lastID: last.id,
                                     lastText: last.text,
                                     lastDone: last.done)
    }
}

struct VTTCue: Equatable {
    var start: Double
    var end: Double
    var text: String
}

struct LyricCueGroup: Identifiable, Equatable {
    let id: Int
    var start: Double
    var end: Double
    var text: String
    var firstCueIndex: Int
    var lastCueIndex: Int
    var lineCount: Int
    var runeCount: Int
    var speaker: String
}

struct DownloadedPodcastFile: Identifiable, Equatable {
    let id = UUID()
    let url: URL
}

/// Drives the live podcast experience: streams the audio (live HLS while
/// generating, final MP3 when ready), surfaces the per-agent transcript over the
/// job WebSocket, polls live captions, and tracks job completion.
@MainActor
@Observable
final class PlayerModel {
    // The backend now emits zero-bias VTT for audio-only feeds (cues align with
    // the untrimmed recording), so no manual lead is needed. A non-zero value
    // here would make live captions appear early.
    nonisolated static let liveCaptionLeadSeconds = 0.0
    nonisolated static let audioBookChapterTitleLeadSeconds = 2.0
    nonisolated static let lyricGroupMinRunes = 28
    nonisolated static let lyricGroupMaxRunes = 96
    nonisolated static let lyricGroupMaxLines = 3
    nonisolated static let lyricGroupMaxGapSeconds = 1.25
    nonisolated static let lyricGroupMaxDurationSeconds = 12.0
    nonisolated static let minimumTranscriptLoadingSeconds = 1.0

    var discussion: Discussion
    var uiActionsRefreshVersion = 0
    /// Exposed (read-only use) so views like the share sheet can reuse the same
    /// authenticated client instead of constructing another.
    let api: APIClient
    let username: String
    /// The current participant's authenticated id. Exposed so the transcript can
    /// reliably tell *this* user's own messages apart from other participants' —
    /// both persist with `isUser == true`, but each user line now carries a
    /// server-owned `senderUserID`. Empty only when signed out / unknown.
    let currentUserID: String
    /// The current participant's display name. Retained as a legacy fallback for
    /// lines persisted before `senderUserID` existed (no sender id to compare).
    var currentUsername: String { username }
    /// Set when this discussion was opened via a share link; authorizes
    /// a non-owner participant's comments on the backend.
    let shareToken: String?

    /// Two prominent colors derived from the cover (gradient hexes, or extracted
    /// from the cover image) used to tint the full-screen player background.
    var coverColors: [Color] = []
    var coverColorsSourceKey: String?

    let player = AVPlayer()
    var isPlaying = false
    var currentTime: Double = 0
    var duration: Double = 0
    var elapsedTime: Double = 0
    var remainingTime: Double = 0
    var caption: String = ""
    var captionSpeaker: String = ""
    var phaseLabel: String = ""
    var statusText: String = ""
    var isForceStopping = false
    var isFinished = false
    var downloadURL: URL?
    var isDownloadingPodcast = false
    var downloadProgress = 0.0
    var downloadErrorText: String?
    var showsDownloadDialog = false
    var downloadedPodcastFile: DownloadedPodcastFile?
    var isTranscriptLoading = false
    var lines: [LiveLine] = []
    var transcriptScrollToken: TranscriptScrollToken {
        TranscriptScrollToken.make(for: lines)
    }
    var canForceStop: Bool {
        discussion.isOwner == true && discussion.status == .generating && !isFinished && !isForceStopping && discussion.jobID != nil
    }
    var showsForceStopAction: Bool {
        discussion.isOwner == true && discussion.status == .generating && !isFinished && discussion.jobID != nil
    }
    var isReadyForDownload: Bool {
        discussion.status == .ready || (isFinished && downloadURL != nil)
    }
    var canDownloadPodcast: Bool {
        isReadyForDownload && (downloadURL != nil || discussion.jobID != nil)
    }
    var canSendMessages: Bool {
        !isFinished && discussion.canSendMessages
    }
    var showsPodcastActions: Bool {
        showsForceStopAction || canDownloadPodcast
    }
    var canSeek: Bool {
        !usesLiveCaptionTiming && duration > 0
    }

    var socket: JobSocket?
    var isStartingJobSocket = false
    var timeObserver: Any?
    var itemStatusObservation: NSKeyValueObservation?
    var playbackRetryTask: Task<Void, Never>?
    var playbackJobID: String?
    var playbackRetryCount = 0
    var remoteCommandTargets: [Any] = []
    nonisolated(unsafe) var audioInterruptionObserver: NSObjectProtocol?
    var isAudioSessionInterrupted = false
    var shouldResumeAfterAudioInterruption = false
    var nowPlayingArtwork: MPMediaItemArtwork?
    var nowPlayingArtworkSourceKey: String?
    var usesLiveCaptionTiming = false
    var autoplayRequested = false
    /// Whether playback should auto-start once the item is ready. Captured at
    /// `start()` from the entry status: a podcast that was already ready when
    /// the user opened it stays paused; a live/generating entry autoplays even
    /// if the job finishes (and final audio is installed) mid-setup or on retry.
    var autoplayOnEntry = true
    /// A seek to apply once the current item reaches `.readyToPlay`. Seeking a
    /// freshly-replaced item before it loads is dropped (or lands past the end),
    /// which is exactly what wedged the swap-to-final-audio playback.
    var pendingResumeTime: Double?
    /// Set once the final (seekable) audio item has been installed, so the live
    /// `setupPlayer` task can't clobber it with a now-dead HLS item if the job
    /// finishes during one of its `await` suspensions.
    var finalAudioInstalled = false
    var cues: [VTTCue] = []
    var activeLyricGroupID: Int?
    var illustrationTimeline: [IllustrationCue] = []
    var tasks: [Task<Void, Never>] = []
    var transcriptLoadingStartedAt: Date?
    var transcriptLoadingHideTask: Task<Void, Never>?

    /// Lyrics mode is available once we have final (seekable) caption timing and
    /// at least one cue. While streaming we only surface the current caption.
    var supportsLyrics: Bool { !usesLiveCaptionTiming && !cues.isEmpty }

    // Memoized lyric groups. Grouping is O(cues × lines) — every cue scans the
    // transcript for its speaker — so recomputing it on each access made the
    // full-screen lyrics list lag with a large script (the list body and the
    // 4 Hz `activeLyricGroupID` update both read it). Lyrics mode only runs on
    // final, stable captions, so we rebuild only when the cue/line counts move.
    @ObservationIgnored var lyricGroupsCache: [LyricCueGroup] = []
    @ObservationIgnored var lyricGroupsCacheKey = (-1, -1)

    init(discussion: Discussion, api: APIClient, username: String, userID: String = "",
         shareToken: String? = nil) {
        self.discussion = discussion
        self.api = api
        self.username = username
        self.currentUserID = userID
        self.shareToken = shareToken
    }

    /// Final, seekable caption list grouped for the lyrics view. Live captions
    /// still read the raw cue list so streaming timing stays one cue at a time.
    var lyricCueGroups: [LyricCueGroup] {
        let key = (cues.count, lines.count)
        if key != lyricGroupsCacheKey {
            lyricGroupsCache = Self.groupLyricCues(cues) { [lines] cue in
                Self.captionSpeaker(for: cue.text, in: lines) ?? ""
            }
            lyricGroupsCacheKey = key
        }
        return lyricGroupsCache
    }

    /// Index of the cue at the current playback time, used to highlight and
    /// auto-scroll the lyrics list. Falls back to the last cue already passed.
    var activeCueIndex: Int? {
        let t = captionLookupTime(playbackTime: currentTime)
        return cues.firstIndex(where: { t >= $0.start && t <= $0.end })
            ?? cues.lastIndex(where: { $0.start <= t })
    }

}
