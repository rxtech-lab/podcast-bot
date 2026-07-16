import AVKit
import Kingfisher
import MarkdownUI
import Photos
import PhotosUI
import RxAuthSwift
import SwiftUI
import TipKit
import UIKit
import UniformTypeIdentifiers
import os

#if DEBUG
struct TranscriptJudgementSourcesPreview: View {
    @State private var selectedSources: TranscriptSourcesSelection?

    private let sourceLine = LiveLine(
        speaker: "韩猎头（资深行业猎头 / 人才智库专家）",
        role: "discussant",
        text: "现在的筛选标准已经变了，企业不再看你做了多少，而是在看你用什么效率做，以及能不能带着 AI 一起做。",
        isUser: false,
        done: true,
        sources: [
            SourceDTO(
                title: "Hiring trends report",
                url: "https://example.com/hiring-trends",
                snippet: "Employers increasingly ask candidates to describe tool fluency and measurable impact.",
                markdown: nil
            ),
            SourceDTO(
                title: "AI skills survey",
                url: "https://example.com/ai-skills",
                snippet: "Survey data on AI tooling expectations in technical interviews.",
                markdown: nil
            ),
        ],
        judgementComment: "这点需要更强的证据支撑，先不要把它当成定论。"
    )

    var body: some View {
        ZStack {
            Theme.background.ignoresSafeArea()
            ScrollView {
                VStack(spacing: 14) {
                    TranscriptBubble(line: sourceLine, isMine: false) { sources in
                        selectedSources = TranscriptSourcesSelection(sources: sources)
                    }
                    TranscriptBubble(
                        line: LiveLine(
                            speaker: "You",
                            role: "user",
                            text: "Can you show the sources behind that?",
                            isUser: true,
                            done: true
                        ),
                        isMine: true
                    )
                }
                .padding(16)
            }
        }
        .sheet(item: $selectedSources) { selection in
            SourcesSheet(
                discussion: Self.previewDiscussion(sources: selection.sources),
                allowsAddingSources: false
            )
        }
    }

    private static func previewDiscussion(sources: [SourceDTO]) -> Discussion {
        var discussion = try! JSONDecoder().decode(
            Discussion.self,
            from: Data("""
            {
              "id": "preview-transcript-sources",
              "topic": "AI changes in hiring",
              "title": "Hiring Signals in the AI Era",
              "status": "ready",
              "language": "zh"
            }
            """.utf8)
        )
        discussion.sources = sources
        return discussion
    }
}

#Preview("PodcastPlayerView · Pin to top") {
    PodcastPinPreview()
}

#Preview("Transcript · Judgement and Sources") {
    TranscriptJudgementSourcesPreview()
}
#endif

/// A small Identifiable wrapper so a bare URL can drive `.fullScreenCover(item:)`.
