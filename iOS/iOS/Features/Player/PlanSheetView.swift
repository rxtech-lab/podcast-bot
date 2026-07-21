import AVKit
import Kingfisher
import MarkdownUI
import Photos
import PhotosUI
import RxAuthSwift
import SwiftUI
import TipKit
#if canImport(UIKit)
import UIKit
#endif
import UniformTypeIdentifiers
import os

struct PlanSheetView: View {
    @Environment(AuthManager.self) private var auth
    @Environment(\.dismiss) private var dismiss
    @State private var discussion: Discussion
    @State private var showingSources = false
    @State private var showingSpeakerModels = false
    @State private var selectedChapters: PlanChaptersPresentation?
    @State private var selectedTranscript: UploadedAudioTranscriptPresentation?
    @State private var planningAttachments: [Attachment] = []
    @State private var selectedAttachment: AttachmentPreviewItem?
    @State private var isLoadingFullPlan = false
    @State private var loadError: String?
    /// Presentation language of the player (nil = source). Forwarded to every
    /// re-fetch so the plan stays translated after loading the full script.
    private let language: String?

    init(discussion: Discussion, language: String? = nil) {
        _discussion = State(initialValue: discussion)
        self.language = language
    }

    var body: some View {
        NavigationStack {
            ZStack {
                Theme.background.ignoresSafeArea()
                ScrollView {
                    VStack(alignment: .leading, spacing: 14) {
                        PlanSnapshotCard(
                            label: "Plan",
                            snapshot: PlanSnapshot(discussion: discussion),
                            onSourcesTapped: { showingSources = true },
                            onChaptersTapped: {
                                let snapshot = PlanSnapshot(discussion: discussion)
                                if snapshot.isUploadedAudio {
                                    selectedTranscript = UploadedAudioTranscriptPresentation(snapshot: snapshot)
                                } else {
                                    selectedChapters = PlanChaptersPresentation(title: snapshot.title, chapters: snapshot.chapters)
                                }
                            },
                            onEditModels: { showingSpeakerModels = true },
                            attachments: planningAttachments,
                            onAttachmentTapped: { attachment in
                                selectedAttachment = AttachmentPreviewItem(attachment: attachment)
                            }
                        )
                        if isLoadingFullPlan && discussion.script == nil {
                            ProgressView()
                                .tint(Theme.accent)
                                .frame(maxWidth: .infinity, alignment: .center)
                                .padding(.top, 12)
                        } else if let loadError, discussion.script == nil {
                            Text(loadError)
                                .font(.footnote)
                                .foregroundStyle(Theme.secondaryText)
                        }
                    }
                    .padding(16)
                }
                .scrollDismissesKeyboard(.interactively)
            }
            .task(id: discussion.id) {
                await fetchFullPlanIfNeeded()
                await fetchPlanningAttachments()
            }
            .navigationTitle("Plan")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button {
                        dismiss()
                    } label: {
                        Image(systemName: "xmark")
                    }
                    .accessibilityLabel("Close")
                }
            }
            .sheet(isPresented: $showingSources) {
                SourcesSheet(
                    discussion: discussion,
                    allowsAddingSources: false
                )
            }
            .sheet(isPresented: $showingSpeakerModels) {
                SpeakerModelsSheet(discussion: $discussion, allowsEditing: false, language: language)
            }
            .sheet(item: $selectedChapters) { presentation in
                AudioBookChaptersSheet(presentation: presentation)
            }
            .sheet(item: $selectedTranscript) { presentation in
                UploadedAudioTranscriptSheet(
                    discussionID: discussion.id,
                    presentation: presentation,
                    allowsEditing: false
                )
            }
            .sheet(item: $selectedAttachment) { item in
                AttachmentPreviewSheet(attachment: item.attachment)
            }
        }
    }

    private func fetchFullPlanIfNeeded() async {
        guard discussion.script == nil else { return }
        isLoadingFullPlan = true
        defer { isLoadingFullPlan = false }
        do {
            discussion = try await APIClient(tokens: auth).discussion(id: discussion.id, language: language)
            loadError = nil
        } catch {
            guard !APIClient.isCancellation(error) else { return }
            loadError = (error as? APIError)?.errorDescription ?? error.localizedDescription
        }
    }

    /// Loads the documents/audio the user attached while planning so the plan
    /// stays inspectable after generation starts. Best-effort: a discussion
    /// without a planning conversation (404) simply shows no section. The
    /// server re-signs fresh attachment URLs on every fetch, so previews work
    /// even long after the original presigned URLs expired.
    private func fetchPlanningAttachments() async {
        guard let view = try? await APIClient(tokens: auth).planningConversation(id: discussion.id) else { return }
        var seen = Set<String>()
        var collected: [Attachment] = []
        for part in view.parts where part.kind == "text" && part.role == "user" {
            for attachment in part.attachments ?? [] {
                let hasContent = !(attachment.markdown ?? "").isEmpty
                    || !(attachment.url ?? "").isEmpty
                    || !(attachment.key ?? "").isEmpty
                guard hasContent else { continue }
                let dedupKey = attachment.key ?? attachment.filename
                guard !dedupKey.isEmpty, seen.insert(dedupKey).inserted else { continue }
                collected.append(attachment)
            }
        }
        planningAttachments = collected
    }
}

/// A row in the transcript `MessageList`: either a live transcript line or the
/// trailing points-summary accessory.
