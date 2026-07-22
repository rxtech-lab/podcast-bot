import SwiftUI

/// Tabs on the home screen. Search remains a trailing tab on iOS; macOS uses
/// `.searchable` directly from Home instead.
enum HomeTab {
    case home
    case chat
    case search
}
