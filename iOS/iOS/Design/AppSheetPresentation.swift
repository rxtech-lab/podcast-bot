import SwiftUI

extension View {
    /// Default presentation policy for app-owned sheets. Sheets should provide
    /// an explicit Done or Cancel action instead of relying on swipe dismissal.
    func appSheetPresentation() -> some View {
        interactiveDismissDisabled(true)
    }
}
