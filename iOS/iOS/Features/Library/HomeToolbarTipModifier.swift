import Kingfisher
import RxAuthSwift
import SwiftUI
import TipKit
#if canImport(UIKit)
import UIKit
#endif

struct HomeToolbarTipModifier: ViewModifier {
    let itemID: String

    @ViewBuilder
    func body(content: Content) -> some View {
        if itemID == "market" {
            content.popoverTip(OpenMarketTip(), arrowEdge: .top)
        } else {
            content
        }
    }
}
