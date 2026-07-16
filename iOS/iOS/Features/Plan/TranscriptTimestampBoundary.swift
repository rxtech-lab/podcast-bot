import AVFoundation
import Observation
import SwiftUI

enum TranscriptTimestampBoundary: String, CaseIterable, Identifiable {
    case start
    case end

    var id: String { rawValue }
    var title: LocalizedStringKey { self == .start ? "Start" : "End" }
    var adjustmentTitle: LocalizedStringKey {
        self == .start ? "Adjust Start Time" : "Adjust End Time"
    }
    var selectionTitle: LocalizedStringKey {
        self == .start ? "Select Start Time" : "Select End Time"
    }
}
