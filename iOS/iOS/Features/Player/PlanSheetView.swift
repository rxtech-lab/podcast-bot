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

struct PlanSheetView: View {
    @Environment(AuthManager.self) private var auth
    @Environment(\.dismiss) private var dismiss
    @State private var discussion: Discussion
    @State private var showingSources = false
    @State private var showingSpeakerModels = false
    @State private var selectedChapters: PlanChaptersPresentation?
    @State private var selectedTranscript: UploadedAudioTranscriptPresentation?
    @State private var isLoadingFullPlan = false
    @State private var loadError: String?

    init(discussion: Discussion) {
        _discussion = State(initialValue: discussion)
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
                            onEditModels: { showingSpeakerModels = true }
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
                SpeakerModelsSheet(discussion: $discussion, allowsEditing: false)
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
        }
    }

    private func fetchFullPlanIfNeeded() async {
        guard discussion.script == nil else { return }
        isLoadingFullPlan = true
        defer { isLoadingFullPlan = false }
        do {
            discussion = try await APIClient(tokens: auth).discussion(id: discussion.id)
            loadError = nil
        } catch {
            guard !APIClient.isCancellation(error) else { return }
            loadError = (error as? APIError)?.errorDescription ?? error.localizedDescription
        }
    }
}

/// A row in the transcript `MessageList`: either a live transcript line or the
/// trailing points-summary accessory.
