//
//  Config.swift
//  iOS
//
//  Created by Qiwei Li on 6/23/26.
//

import Foundation
import SwiftUI
import TipKit

/// Centralized brand strings. Use the `LocalizedStringKey` variants in SwiftUI
/// views (Text, Label, navigationTitle, etc.) and the `Raw` String variants in
/// code, interpolation, and APIs that take `String`.
enum AppStringLiteral {
    static let appTitleRaw = "PodcastFM"
    static var appTitle: LocalizedStringKey { LocalizedStringKey(appTitleRaw) }

    static let stationTitleRaw = "Stations"
    static var stationTitle: LocalizedStringKey { LocalizedStringKey(stationTitleRaw) }

    static let stationNameRaw = "Stations"
    static var stationName: LocalizedStringKey { LocalizedStringKey(stationNameRaw) }

    static let stationsNameRaw = "Staions"
    static var stationsName: LocalizedStringKey { LocalizedStringKey(stationsNameRaw) }
}

struct PlanGenerateTip: Tip {
    var title: Text {
        Text("Generate the audio")
    }

    var message: Text? {
        Text("Turn this plan into a live podcast when the outline is ready.")
    }

    var image: Image? {
        Image(systemName: "waveform")
    }

    var options: [any TipOption] {
        Tips.MaxDisplayCount(1)
    }
}

struct NewDiscussionPlanTip: Tip {
    var title: Text {
        Text("Plan the discussion")
    }

    var message: Text? {
        Text("Create the outline first, then edit speakers, sources, and language before generating audio.")
    }

    var image: Image? {
        Image(systemName: "doc.text")
    }

    var options: [any TipOption] {
        Tips.MaxDisplayCount(1)
    }
}

struct SendAudioTip: Tip {
    var title: Text {
        Text("Send audio")
    }

    var message: Text? {
        Text("Open this menu to record a voice message, review the transcript, then send it to the discussion.")
    }

    var image: Image? {
        Image(systemName: "mic.fill")
    }

    var options: [any TipOption] {
        Tips.MaxDisplayCount(1)
    }
}

struct PodcastPlanTip: Tip {
    var title: Text {
        Text("View the plan")
    }

    var message: Text? {
        Text("Open the original discussion plan, sources, and speaker details for this podcast.")
    }

    var image: Image? {
        Image(systemName: "doc.text")
    }

    var options: [any TipOption] {
        Tips.MaxDisplayCount(1)
    }
}

struct FullScreenCaptionTip: Tip {
    private static let titleKey = LocalizedStringKey("Show captions")
    private static let messageKey = LocalizedStringKey("Switch between the cover art and synced captions while listening full screen.")

    var title: Text {
        Text(Self.titleKey)
    }

    var message: Text? {
        Text(Self.messageKey)
    }

    var image: Image? {
        Image(systemName: "quote.bubble.fill")
    }

    var options: [any TipOption] {
        Tips.MaxDisplayCount(1)
    }
}

struct PublishToMarketTip: Tip {
    var title: Text {
        Text("Make it public")
    }

    var message: Text? {
        Text("Use the actions menu to publish this podcast to Market or make a public podcast private again.")
    }

    var image: Image? {
        Image(systemName: "globe")
    }

    var options: [any TipOption] {
        Tips.MaxDisplayCount(1)
    }
}

struct GenerateCoverTip: Tip {
    var title: Text {
        Text("Generate a cover")
    }

    var message: Text? {
        Text("Choose AI, write a prompt, and generate cover art on the server.")
    }

    var image: Image? {
        Image(systemName: "sparkles")
    }

    var options: [any TipOption] {
        Tips.MaxDisplayCount(1)
    }
}

struct OpenMarketTip: Tip {
    var title: Text {
        Text("Browse Market")
    }

    var message: Text? {
        Text("Discover public podcasts, listen, like, and save stations from other creators.")
    }

    var image: Image? {
        Image(systemName: "square.grid.2x2.fill")
    }

    var options: [any TipOption] {
        Tips.MaxDisplayCount(1)
    }
}

struct ShareStationTip: Tip {
    var title: Text {
        Text("Share with others")
    }

    var message: Text? {
        Text("Public podcasts share directly. Private podcasts can use expiring links that you can revoke.")
    }

    var image: Image? {
        Image(systemName: "square.and.arrow.up")
    }

    var options: [any TipOption] {
        Tips.MaxDisplayCount(1)
    }
}

struct SummaryPDFDownloadTip: Tip {
    var title: Text {
        Text("Save the summary")
    }

    var message: Text? {
        Text("Open this menu to download the summary as a PDF or Markdown file.")
    }

    var image: Image? {
        Image(systemName: "arrow.down.doc")
    }

    var options: [any TipOption] {
        Tips.MaxDisplayCount(1)
    }
}

struct TranscriptEditingTip: Tip {
    var title: Text {
        Text("Edit Transcript")
    }

    var message: Text? {
        Text("Swipe left on a segment to edit its text or retime it against the uploaded audio.")
    }

    var image: Image? {
        Image(systemName: "hand.draw.fill")
    }

    var options: [any TipOption] {
        Tips.MaxDisplayCount(1)
    }
}
