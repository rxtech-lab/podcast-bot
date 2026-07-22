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

struct PodcastLoadingMenu: View {
    let showsPoints: Bool
    let pointsMenuLabel: String
    let onShowPoints: () -> Void
    let isCreatingFromPlan: Bool
    let onCreateFromPlan: (() -> Void)?
    var onSignOut: (() -> Void)?

    var body: some View {
        Menu {
            if showsPoints {
                Button {
                    onShowPoints()
                } label: {
                    Label(pointsMenuLabel, systemImage: "sparkles")
                }
            }
            if showsPoints && onCreateFromPlan != nil {
                Divider()
            }
            if let onCreateFromPlan {
                Button(action: onCreateFromPlan) {
                    Label(isCreatingFromPlan ? "Creating" : "Create from Plan",
                          systemImage: isCreatingFromPlan ? "hourglass" : "plus.circle")
                }
                .disabled(isCreatingFromPlan)
            }
            if let onSignOut {
                if showsPoints || onCreateFromPlan != nil {
                    Divider()
                }
                Button(role: .destructive, action: onSignOut) {
                    Label("Sign Out", systemImage: "rectangle.portrait.and.arrow.right")
                }
            }
        } label: {
            Image(systemName: "ellipsis.circle")
        }
        .accessibilityLabel("\(AppStringLiteral.stationNameRaw) actions")
    }
}
