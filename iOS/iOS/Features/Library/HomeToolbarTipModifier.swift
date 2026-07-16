import Kingfisher
import RxAuthSwift
import SwiftUI
import TipKit
import UIKit

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
