import Kingfisher
import RxAuthSwift
import SwiftUI
import TipKit
#if canImport(UIKit)
import UIKit
#endif

struct SettingsRowLabel: View {
    let title: String
    let systemImage: String

    var body: some View {
        HStack(spacing: 12) {
            Image(systemName: systemImage)
                .font(.body.weight(.semibold))
                .foregroundStyle(Theme.accent)
                .frame(width: 26, height: 26)
            Text(title)
                .foregroundStyle(.primary)
            Spacer()
        }
    }
}
