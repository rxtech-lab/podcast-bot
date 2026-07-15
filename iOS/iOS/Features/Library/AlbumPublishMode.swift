import Kingfisher
import SwiftUI

enum AlbumPublishMode: String, CaseIterable, Identifiable {
    case all
    case selected

    var id: String { rawValue }

    var title: String {
        switch self {
        case .all:
            return "All Podcasts"
        case .selected:
            return "Selected"
        }
    }
}


