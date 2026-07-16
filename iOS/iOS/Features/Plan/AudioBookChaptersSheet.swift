import AVFoundation
import Observation
import SwiftUI

struct AudioBookChaptersSheet: View {
    @Environment(\.dismiss) private var dismiss

    let presentation: PlanChaptersPresentation

    var body: some View {
        NavigationStack {
            List {
                ForEach(presentation.chapters) { chapter in
                    VStack(alignment: .leading, spacing: 8) {
                        HStack(alignment: .firstTextBaseline, spacing: 10) {
                            Text("\(chapter.number)")
                                .font(.caption.weight(.bold))
                                .foregroundStyle(.white)
                                .frame(width: 24, height: 24)
                                .background(Theme.accent, in: .circle)
                            Text(chapter.title)
                                .font(.body.weight(.semibold))
                                .foregroundStyle(.primary)
                                .fixedSize(horizontal: false, vertical: true)
                        }
                        if !chapter.summary.isEmpty {
                            Text(chapter.summary)
                                .font(.subheadline)
                                .foregroundStyle(Theme.secondaryText)
                                .fixedSize(horizontal: false, vertical: true)
                        }
                    }
                    .padding(.vertical, 6)
                }
            }
            .navigationTitle(presentation.title.isEmpty ? "Chapters" : presentation.title)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }
                }
            }
        }
        .presentationDetents([.medium, .large])
    }
}

/// Transcript-specific companion to `AudioBookChaptersSheet`. Each row can
/// replay exactly its source-audio time range; editable plan screens also add a
/// trailing swipe action for correcting the speaker, timing, and content.


