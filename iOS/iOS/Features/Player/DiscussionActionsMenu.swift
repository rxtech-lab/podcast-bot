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

struct DiscussionActionsMenu: View {
    let items: [DiscussionUIActionItem]
    let labelSystemImage: String
    let accessibilityLabel: String
    var titleOverride: (DiscussionUIActionItem) -> String? = { _ in nil }
    let isBusy: (DiscussionUIActionItem) -> Bool
    let perform: (DiscussionUIActionItem) -> Void

    var body: some View {
        Menu {
            if items.isEmpty {
                Label("Loading", systemImage: "hourglass")
                    .foregroundStyle(.secondary)
            } else {
                ForEach(items) { item in
                    actionRow(item)
                }
            }
        } label: {
            Image(systemName: labelSystemImage)
        }
        .accessibilityLabel(accessibilityLabel)
    }

    @ViewBuilder
    private func actionRow(_ item: DiscussionUIActionItem) -> some View {
        if item.children.count > 1 {
            Menu {
                ForEach(item.children) { child in
                    leafActionRow(child)
                }
            } label: {
                rowLabel(item, busy: false)
            }
            .disabled(!item.enabled)
        } else if let child = item.children.first {
            leafActionRow(child)
        } else {
            leafActionRow(item)
        }
    }

    @ViewBuilder
    private func leafActionRow(_ item: DiscussionUIActionItem) -> some View {
        if item.isDivider {
            Divider()
        } else {
            let busy = isBusy(item)
            let disabled = !item.enabled || busy
            if item.action.type == "share-link", let url = URL(string: item.action.link) {
                ShareLink(item: url) {
                    rowLabel(item, busy: busy)
                }
                .disabled(disabled)
            } else {
                Button(role: buttonRole(for: item)) {
                    perform(item)
                } label: {
                    rowLabel(item, busy: busy)
                }
                .disabled(disabled)
            }
        }
    }

    @ViewBuilder
    private func rowLabel(_ item: DiscussionUIActionItem, busy: Bool) -> some View {
        let title = busy ? (item.loadingTitle ?? titleOverride(item) ?? item.title) : (titleOverride(item) ?? item.title)
        if let systemImage = item.systemImage, !systemImage.isEmpty {
            Label(title, systemImage: busy && item.loadingTitle != nil ? "hourglass" : systemImage)
        } else {
            Text(title)
        }
    }

    private func buttonRole(for item: DiscussionUIActionItem) -> ButtonRole? {
        item.role == "destructive" ? .destructive : nil
    }
}
