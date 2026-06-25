import SwiftUI

/// Inline card for a generic tool call (search_sources / crawl_sources / other)
/// in the planning conversation. Tapping opens a read-only detail sheet. Plan and
/// question tool calls are rendered by dedicated views instead.
struct PlanningToolCard: View {
    let part: PlanningPart
    var onTap: () -> Void = {}

    var body: some View {
        Button(action: onTap) {
            HStack(spacing: 10) {
                Image(systemName: icon)
                    .font(.callout)
                    .foregroundStyle(iconColor)
                    .frame(width: 22)
                VStack(alignment: .leading, spacing: 2) {
                    Text(title)
                        .font(.subheadline.weight(.medium))
                        .foregroundStyle(.primary)
                    Text(statusText)
                        .font(.caption)
                        .foregroundStyle(Theme.secondaryText)
                }
                Spacer(minLength: 8)
                if status == .running {
                    ProgressView().controlSize(.small)
                } else {
                    Image(systemName: "chevron.right")
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(Theme.secondaryText)
                }
            }
            .padding(12)
            .frame(maxWidth: 280, alignment: .leading)
            .background(Theme.agentBubble, in: .rect(cornerRadius: 14))
        }
        .buttonStyle(.plain)
    }

    private enum Status { case running, completed, failed }

    private var status: Status {
        switch part.status {
        case "failed": return .failed
        case "running": return .running
        default: return .completed
        }
    }

    private var icon: String {
        switch part.toolName {
        case "search_sources": return "magnifyingglass"
        case "crawl_sources": return "link"
        default:
            switch status {
            case .completed: return "checkmark.circle.fill"
            case .failed: return "xmark.circle.fill"
            case .running: return "arrow.trianglehead.2.clockwise"
            }
        }
    }

    private var iconColor: Color {
        switch status {
        case .completed: return .green
        case .failed: return .red
        case .running: return Theme.accent
        }
    }

    private var title: String {
        switch part.toolName {
        case "search_sources": return String(localized: "Searched the web", comment: "Tool card title for a web search step")
        case "crawl_sources": return String(localized: "Read links", comment: "Tool card title for a URL read step")
        default: return part.toolName ?? String(localized: "Tool", comment: "Generic tool card title")
        }
    }

    private var statusText: String {
        switch status {
        case .running: return String(localized: "Running…", comment: "Tool card status while a tool is running")
        case .completed: return String(localized: "Tap to view details", comment: "Tool card hint to open the detail sheet")
        case .failed: return String(localized: "Failed", comment: "Tool card status when a tool failed")
        }
    }
}

/// Read-only sheet showing a tool call's input arguments and its result text.
struct PlanningToolDetailSheet: View {
    let part: PlanningPart
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 20) {
                    if let input = part.input {
                        section(title: String(localized: "Input", comment: "Tool detail sheet section for the tool's input")) {
                            Text(input.prettyString)
                        }
                    } else if let input = part.inputText, !input.isEmpty {
                        section(title: String(localized: "Input", comment: "Tool detail sheet section for the tool's input")) {
                            Text(input)
                        }
                    }
                    if let result = part.resultText, !result.isEmpty {
                        section(title: String(localized: "Result", comment: "Tool detail sheet section for the tool's result")) {
                            Text(result)
                        }
                    }
                }
                .padding(20)
            }
            .background(Theme.background.ignoresSafeArea())
            .navigationTitle(part.toolName ?? "Tool")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button(String(localized: "Done", comment: "Dismiss the tool detail sheet")) { dismiss() }
                }
            }
        }
        .presentationDetents([.medium, .large])
        .presentationDragIndicator(.visible)
    }

    @ViewBuilder
    private func section<Content: View>(title: String, @ViewBuilder content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(title)
                .font(.caption.weight(.semibold))
                .foregroundStyle(Theme.secondaryText)
                .textCase(.uppercase)
            content()
                .font(.system(.footnote, design: .monospaced))
                .foregroundStyle(.primary)
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(12)
                .background(Theme.agentBubble, in: .rect(cornerRadius: 12))
        }
    }
}
