import Kingfisher
import SwiftUI

struct MarketStatusLabel: View {
    let discussion: Discussion

    var body: some View {
        Label(title, systemImage: icon)
            .font(.caption.weight(.semibold))
            .foregroundStyle(discussion.status == .generating ? .green : Theme.secondaryText)
    }

    private var title: String {
        switch discussion.status {
        case .generating: return "Live"
        case .ready: return "Ready"
        case .planning: return "Planning"
        case .failed: return "Failed"
        }
    }

    private var icon: String {
        discussion.status == .generating ? "dot.radiowaves.left.and.right" : "play.circle"
    }
}


