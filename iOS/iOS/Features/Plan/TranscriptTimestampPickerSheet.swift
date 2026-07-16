import SwiftUI

struct TranscriptTimestampPickerSheet: View {
    @Environment(\.dismiss) private var dismiss

    let boundary: TranscriptTimestampBoundary
    let maximumMs: Int64
    let onApply: (Int64) -> Void

    @State private var draftMilliseconds: Int64

    init(
        boundary: TranscriptTimestampBoundary,
        milliseconds: Int64,
        maximumMs: Int64,
        onApply: @escaping (Int64) -> Void
    ) {
        self.boundary = boundary
        self.maximumMs = maximumMs
        self.onApply = onApply
        _draftMilliseconds = State(initialValue: milliseconds)
    }

    var body: some View {
        NavigationStack {
            VStack(spacing: 12) {
                Text(transcriptRetimeTimestamp(draftMilliseconds))
                    .font(.title3.monospacedDigit().weight(.semibold))

                TranscriptTimestampWheel(
                    milliseconds: $draftMilliseconds,
                    maximumMs: maximumMs
                )
            }
            .padding(.horizontal, 16)
            .navigationTitle(boundary.adjustmentTitle)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                        .accessibilityIdentifier("retime.picker.cancel")
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") {
                        onApply(min(max(draftMilliseconds, 0), maximumMs))
                        dismiss()
                    }
                    .accessibilityIdentifier("retime.picker.done")
                }
            }
        }
        .presentationDetents([.medium])
    }
}

func transcriptRetimeTimestamp(_ milliseconds: Int64) -> String {
    let clamped = max(milliseconds, 0)
    let totalSeconds = clamped / 1_000
    let hours = totalSeconds / 3_600
    let minutes = (totalSeconds % 3_600) / 60
    let seconds = totalSeconds % 60
    let fraction = clamped % 1_000
    return String(format: "%02d:%02d:%02d:%03d", hours, minutes, seconds, fraction)
}
