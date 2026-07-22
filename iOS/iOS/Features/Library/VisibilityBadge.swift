import Kingfisher
import RxAuthSwift
import SwiftUI
import TipKit
#if canImport(UIKit)
import UIKit
#endif

struct VisibilityBadge: View {
    let isPublic: Bool

    var body: some View {
        Label(isPublic ? "Public" : "Private", systemImage: isPublic ? "globe" : "lock.fill")
            .font(.caption2.weight(.semibold))
            .foregroundStyle(isPublic ? Theme.accent : Theme.secondaryText)
            .lineLimit(1)
            .fixedSize(horizontal: true, vertical: false)
    }
}
