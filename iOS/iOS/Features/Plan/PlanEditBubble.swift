import SwiftUI
import TipKit

struct PlanEditBubble: View {
    let turn: PlanEditTurn
    var progressText: String? = nil
    var onEditModels: () -> Void = {}
    var onChaptersTapped: (PlanSnapshot) -> Void = { _ in }
    var onSourcesTapped: () -> Void

    var body: some View {
        HStack(alignment: .bottom) {
            if turn.role == .user {
                Spacer(minLength: 46)
            }

            content

            if turn.role != .user {
                Spacer(minLength: 34)
            }
        }
        .frame(maxWidth: .infinity, alignment: turn.role == .user ? .trailing : .leading)
    }

    @ViewBuilder
    private var content: some View {
        switch turn.role {
        case .user:
            Text(turn.text ?? "")
                .font(.body)
                .foregroundStyle(.white)
                .padding(.horizontal, 14)
                .padding(.vertical, 11)
                .background(Theme.accent, in: .rect(cornerRadius: 20))
        case .plan:
            if let snapshot = turn.snapshot {
                PlanSnapshotCard(label: turn.label ?? "Plan", snapshot: snapshot,
                                 onSourcesTapped: onSourcesTapped,
                                 onChaptersTapped: snapshot.chapters.isEmpty ? nil : { onChaptersTapped(snapshot) },
                                 onEditModels: onEditModels)
                    .padding(14)
                    .background(Theme.agentBubble, in: .rect(cornerRadius: 22))
            }
        case .loading:
            HStack(spacing: 10) {
                ProgressView().tint(Theme.accent)
                Text(progressText ?? "Updating plan...")
                    .font(.callout)
                    .foregroundStyle(Theme.secondaryText)
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 11)
            .background(Theme.agentBubble, in: .rect(cornerRadius: 20))
        case .error:
            Text(turn.text ?? "Could not update the plan.")
                .font(.callout)
                .foregroundStyle(.red)
                .padding(.horizontal, 14)
                .padding(.vertical, 11)
                .background(Color.red.opacity(0.12), in: .rect(cornerRadius: 20))
        }
    }

}

