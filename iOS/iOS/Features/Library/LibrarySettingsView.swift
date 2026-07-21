import Kingfisher
import RxAuthSwift
import SwiftUI
import TipKit
#if canImport(UIKit)
import UIKit
#endif

struct LibrarySettingsView: View {
    @Environment(\.dismiss) var dismiss
    let userName: String?
    let userID: String?
    let canManageSubscription: Bool
    let pointsLabel: String?
    @State var didCopyUserID = false
    /// Preferred chapters per audiobook generation batch. The server hard-caps
    /// a batch at 5; this only controls how many chapters the checklist
    /// preselects.
    @AppStorage("audiobook.defaultBatchChapters") var audiobookBatchChapters = 3

    var displayName: String {
        let trimmed = userName?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return trimmed.isEmpty ? String(localized: "User", comment: "Fallback account display name") : trimmed
    }

    var displayUserID: String {
        let trimmed = userID?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return trimmed.isEmpty ? String(localized: "Unknown", comment: "Fallback user id in settings") : trimmed
    }

    var avatarInitial: String {
        String(displayName.trimmingCharacters(in: .whitespacesAndNewlines).prefix(1)).uppercased()
    }

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    accountHeader
                }

                Section("Account") {
                    userIDRow
                }

                Section {
                    Stepper(value: $audiobookBatchChapters, in: 1...5) {
                        SettingsRowLabel(title: String(localized: "Max chapters per generation: \(audiobookBatchChapters)"),
                                         systemImage: "text.book.closed")
                    }
                    .accessibilityIdentifier("settings.maxChaptersPerGeneration")
                } header: {
                    Text("Audiobooks")
                } footer: {
                    Text("Long audiobooks generate in batches of up to 5 chapters. Remaining chapters can be generated later from the podcast.")
                }

                if canManageSubscription {
                    Section("Subscription") {
                        if let pointsLabel {
                            NavigationLink {
                                PointsHistoryView(embedsInNavigationStack: false, showsCloseButton: false)
                            } label: {
                                SettingsRowLabel(title: pointsLabel, systemImage: "sparkles")
                            }
                        }

                        NavigationLink {
                            CustomerCenterScreen(showsCloseButton: false)
                                .navigationTitle("Manage Subscription")
                                .navigationBarTitleDisplayMode(.inline)
                        } label: {
                            SettingsRowLabel(title: "Manage Subscription", systemImage: "creditcard")
                        }
                    }
                }
            }
            .formStyle(.grouped)
            .scrollContentBackground(.hidden)
            .background(Theme.background.ignoresSafeArea())
            .navigationTitle("Settings")
            .navigationBarTitleDisplayMode(.large)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button("Done") { dismiss() }
                }
            }
        }
    }

    var accountHeader: some View {
        HStack(spacing: 16) {
            ZStack {
                Circle()
                    .fill(Theme.accent.opacity(0.16))
                Text(avatarInitial)
                    .font(.system(size: 28, weight: .semibold))
                    .foregroundStyle(Theme.accent)
            }
            .frame(width: 64, height: 64)

            VStack(alignment: .leading, spacing: 4) {
                Text(displayName)
                    .font(.title3.weight(.semibold))
                    .lineLimit(2)
                    .minimumScaleFactor(0.85)
                Text("Signed in")
                    .font(.subheadline)
                    .foregroundStyle(Theme.secondaryText)
            }

            Spacer(minLength: 0)
        }
        .padding(.vertical, 8)
    }

    var userIDRow: some View {
        HStack(spacing: 12) {
            VStack(alignment: .leading, spacing: 4) {
                Text("User ID")
                    .font(.body)
                Text(displayUserID)
                    .font(.caption.monospaced())
                    .foregroundStyle(Theme.secondaryText)
                    .lineLimit(2)
                    .textSelection(.enabled)
            }

            Spacer(minLength: 8)

            Button {
                #if canImport(UIKit)
                UIPasteboard.general.string = displayUserID
                #else
                NSPasteboard.general.clearContents()
                NSPasteboard.general.setString(displayUserID, forType: .string)
                #endif
                didCopyUserID = true
            } label: {
                Image(systemName: didCopyUserID ? "checkmark" : "doc.on.doc")
                    .font(.body.weight(.semibold))
            }
            .buttonStyle(.borderless)
            .accessibilityLabel(didCopyUserID ? "Copied User ID" : "Copy User ID")
        }
    }
}
