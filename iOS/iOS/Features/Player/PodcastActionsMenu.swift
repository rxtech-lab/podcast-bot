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

struct PodcastActionsMenu: View {
    @Bindable var model: PlayerModel
    @Environment(EntitlementsManager.self) private var entitlements
    @State private var showingForceStopConfirm = false

    let showsPoints: Bool
    let pointsMenuLabel: String
    let onShowPoints: () -> Void
    let onPublish: () -> Void
    let onEditCover: () -> Void
    let onMakePrivate: () -> Void
    /// Opens the private share sheet (duration picker + manage links). Only
    /// invoked for private discussions; public ones share a plain link inline.
    var onShare: () -> Void = {}
    let onCreateFollowUp: (() -> Void)?
    let isCreatingFromPlan: Bool
    let onCreateFromPlan: (() -> Void)?
    var onSignOut: (() -> Void)?

    /// The plain, permanent public link for a published discussion. The server
    /// builds it (`share_url`, the same `/p/{id}` web-player URL embedded as the
    /// summary's "listen again" link) so the shared link and the markdown link
    /// always match. Falls back to building it locally only if an older server
    /// response omits the field.
    private var publicShareURL: URL {
        if let raw = model.discussion.shareURL,
           let url = URL(string: raw) {
            return url
        }
        return AppConfig.websiteBaseURL.appendingPathComponent("p").appendingPathComponent(model.discussion.id)
    }

    private var actionsTip: (any Tip)? {
        if model.discussion.isPublic {
            return ShareStationTip()
        }
        if model.discussion.isOwner != false {
            return PublishToMarketTip()
        }
        return nil
    }

    var body: some View {
        Menu {
            if showsPoints {
                Button {
                    onShowPoints()
                } label: {
                    Label(pointsMenuLabel, systemImage: "sparkles")
                }
            }
            if showsPoints && (model.showsPodcastActions || onCreateFollowUp != nil || onCreateFromPlan != nil) {
                Divider()
            }
            if let onCreateFollowUp {
                Button(action: onCreateFollowUp) {
                    Label("Create Follow-up", systemImage: "arrow.triangle.branch")
                }
            }
            if let onCreateFromPlan {
                Button(action: onCreateFromPlan) {
                    Label(isCreatingFromPlan ? "Creating" : "Create from Plan",
                          systemImage: isCreatingFromPlan ? "hourglass" : "plus.circle")
                }
                .disabled(isCreatingFromPlan)
            }
            if model.discussion.isOwner != false {
                Button(action: onEditCover) {
                    Label("Edit Cover", systemImage: "photo.badge.plus")
                }
                .disabled(!entitlements.features.canGenerateCoverWithAI)
                if model.discussion.isPublic {
                    Button(role: .destructive, action: onMakePrivate) {
                        Label("Make Private", systemImage: "lock")
                    }
                } else {
                    Button(action: onPublish) {
                        Label("Publish to Market", systemImage: "globe")
                    }
                    .disabled(!entitlements.features.canPublishPodcast)
                }
            }
            // Share: public discussions hand out a plain permanent link; private
            // ones open the duration sheet to mint an expiring, revocable link.
            if model.discussion.isPublic {
                ShareLink(item: publicShareURL) {
                    Label("Share", systemImage: "square.and.arrow.up")
                }
            } else if model.discussion.isOwner != false {
                Button(action: onShare) {
                    Label("Share Link", systemImage: "square.and.arrow.up")
                }
                .disabled(!entitlements.features.canSharePodcastPrivately)
            }
            if model.canDownloadPodcast {
                Button {
                    model.downloadPodcast()
                } label: {
                    Label(model.isDownloadingPodcast ? "Downloading" : "Download \(AppStringLiteral.stationNameRaw)",
                          systemImage: model.isDownloadingPodcast ? "hourglass" : "arrow.down.circle")
                }
                .disabled(model.isDownloadingPodcast)
            } else if model.showsForceStopAction {
                Button(role: .destructive) {
                    showingForceStopConfirm = true
                } label: {
                    Label(model.isForceStopping ? "Finalising" : "Force Stop",
                          systemImage: model.isForceStopping ? "hourglass" : "stop.fill")
                }
                .disabled(!model.canForceStop)
            }
            if let onSignOut {
                if hasNonSignOutActions {
                    Divider()
                }
                Button(role: .destructive, action: onSignOut) {
                    Label("Sign Out", systemImage: "rectangle.portrait.and.arrow.right")
                }
            }
        } label: {
            Image(systemName: "ellipsis")
        }
        .accessibilityIdentifier("player.more")
        .accessibilityLabel("\(AppStringLiteral.stationNameRaw) actions")
        .popoverTip(actionsTip, arrowEdge: .top)
        .confirmationDialog(
            "Force stop this \(AppStringLiteral.stationNameRaw)?",
            isPresented: $showingForceStopConfirm,
            titleVisibility: .visible
        ) {
            Button("Force Stop", role: .destructive) {
                model.forceStop()
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("The current generation will stop after finalising audio that has already been created. New turns will not be generated.")
        }
    }

    private var hasNonSignOutActions: Bool {
        showsPoints
            || onCreateFollowUp != nil
            || onCreateFromPlan != nil
            || model.discussion.isOwner != false
            || model.discussion.isPublic
            || model.canDownloadPodcast
            || model.showsForceStopAction
    }
}
