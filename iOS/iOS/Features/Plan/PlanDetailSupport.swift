import SwiftUI

struct PlanInitialHistoryLoadingView: View {
    var body: some View {
        VStack(spacing: 12) {
            ZStack {
                Circle()
                    .fill(Theme.accent.opacity(0.12))
                    .frame(width: 52, height: 52)
                Image(systemName: "bubble.left.and.bubble.right.fill")
                    .font(.system(size: 28, weight: .semibold))
                    .foregroundStyle(Theme.accent)
            }
            VStack(spacing: 4) {
                Text("Loading \(AppStringLiteral.stationNameRaw)...")
                    .font(.headline)
                Text("Fetching latest messages")
                    .font(.subheadline)
                    .foregroundStyle(Theme.secondaryText)
            }
            ProgressView()
                .tint(Theme.accent)
                .controlSize(.small)
        }
        .multilineTextAlignment(.center)
        .glassCard(cornerRadius: 20)
        .accessibilityElement(children: .combine)
        .accessibilityLabel("Loading \(AppStringLiteral.stationNameRaw)")
    }
}

func planEditRows(from history: [DiscussionEditTurnDTO], discussion: Discussion) -> [PlanEditTurn] {
    history.compactMap { turn in
        switch turn.role {
        case "user":
            let text = (turn.text ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
            return text.isEmpty ? nil : .user(text, id: turn.planEditTurnID)
        case "plan":
            let snapshot = turn.script != nil
                ? PlanSnapshot(turn: turn, topic: discussion.topic)
                : PlanSnapshot(discussion: discussion)
            let label = (turn.text?.isEmpty == false)
                ? turn.text!
                : String(localized: "Plan", comment: "Default label for a plan history card with no stored label")
            return .plan(label: label, snapshot: snapshot, id: turn.planEditTurnID)
        default:
            return nil
        }
    }
}

func planSourceUpdateText(urls: [String]) -> String {
    let count = urls.count
    let header = count == 1
        ? String(localized: "Added \(count) source:", comment: "User bubble header when one source is added; followed by the URL list")
        : String(localized: "Added \(count) sources:", comment: "User bubble header when multiple sources are added; followed by the URL list")
    return ([header] + urls).joined(separator: "\n")
}
