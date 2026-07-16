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
struct PodcastPinPreview: View {
    @State private var lines: [LiveLine] = [
        LiveLine(speaker: "Dr. Lena Ortiz", role: "host", text: "Welcome back. Today we're asking how AI will reshape the classroom over the next decade.", isUser: false, done: true),
        LiveLine(speaker: "Prof. Adeyemi", role: "discussant", text: "Personalized tutoring is the headline, but the research on learning gains is still mixed.", isUser: false, done: true),
        LiveLine(speaker: "Maya Chen", role: "discussant", text: "From the product side, adoption is exploding — the question is whether outcomes follow.", isUser: false, done: true),
    ]
    @State private var message = "What about students who can't afford these tools?"
    @State private var isAtBottom = true
    @State private var shouldScrollToBottom = false
    @State private var replyTask: Task<Void, Never>?

    private var isStreaming: Bool { !(lines.last?.done ?? true) }

    private func items() -> [TranscriptListItem] { lines.map { .line($0, isMine: $0.isUser) } }

    var body: some View {
        ZStack {
            Theme.background.ignoresSafeArea()
            MessageList(
                messages: items(),
                isStreaming: isStreaming,
                shouldScrollToBottom: shouldScrollToBottom,
                isAtBottom: $isAtBottom
            ) { item in
                if case .line(let line, let isMine) = item {
                    TranscriptBubble(line: line, isMine: isMine)
                        .padding(.horizontal, 16)
                        .padding(.vertical, 6)
                }
            }
            .safeAreaInset(edge: .bottom, spacing: 0) { inputBar }
        }
    }

    private var inputBar: some View {
        HStack(spacing: 10) {
            TextField("Send message", text: $message, axis: .vertical)
                .lineLimit(1 ... 3)
                .textFieldStyle(.plain)
            Button(action: send) {
                Image(systemName: "arrow.up.circle.fill")
                    .font(.title2)
                    .foregroundStyle(Theme.accent)
            }
            .disabled(message.trimmingCharacters(in: .whitespaces).isEmpty || isStreaming)
        }
        .padding(12)
        .glassEffect(in: .capsule)
        .padding(16)
    }

    private func send() {
        let text = message.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else { return }
        message = ""
        lines.append(LiveLine(speaker: "You", role: "user", text: text, isUser: true, done: true))
        replyTask?.cancel()
        replyTask = Task { @MainActor in
            try? await Task.sleep(for: .milliseconds(450))
            guard !Task.isCancelled else { return }
            lines.append(LiveLine(speaker: "Dr. Lena Ortiz", role: "host", text: "", isUser: false, done: false))
            let idx = lines.count - 1
            let chunks = [
                "That's the equity question at the heart of this. ",
                "If the best tutors are paywalled, ",
                "AI could widen the gap it promises to close. ",
                "Districts will need procurement and access policies ",
                "before the tools, not after.",
            ]
            var acc = ""
            for chunk in chunks {
                try? await Task.sleep(for: .milliseconds(380))
                guard !Task.isCancelled else { return }
                acc += chunk
                lines[idx].text = acc
            }
            try? await Task.sleep(for: .milliseconds(300))
            guard !Task.isCancelled else { return }
            lines[idx].done = true
        }
    }
}
#endif
