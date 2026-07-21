#if os(macOS)
import SwiftUI

/// Source-compatibility shims so iOS-only SwiftUI APIs used throughout the app
/// compile on macOS without sprinkling `#if os(macOS)` at every call site.
/// Each shim either maps to the closest AppKit-flavored behavior or is a no-op
/// where the concept doesn't exist on the Mac.

/// `navigationBarTitleDisplayMode(.inline/.large)` has no macOS counterpart —
/// the navigation title is always rendered in the window/toolbar style.
enum CompatTitleDisplayMode {
    case automatic, inline, large
}

/// `TextInputAutocapitalization` is UIKit-only; typing on macOS has no
/// system-level autocapitalization toggle per field.
enum CompatTextInputAutocapitalization {
    case never, words, sentences, characters
}

extension View {
    func navigationBarTitleDisplayMode(_ mode: CompatTitleDisplayMode) -> some View {
        self
    }

    func textInputAutocapitalization(_ autocapitalization: CompatTextInputAutocapitalization?) -> some View {
        self
    }

    /// macOS has no full-screen modal presentation; fall back to a sheet.
    func fullScreenCover<Item: Identifiable, Content: View>(
        item: Binding<Item?>,
        onDismiss: (() -> Void)? = nil,
        @ViewBuilder content: @escaping (Item) -> Content
    ) -> some View {
        sheet(item: item, onDismiss: onDismiss, content: content)
    }

    /// macOS has no full-screen modal presentation; fall back to a sheet.
    func fullScreenCover<Content: View>(
        isPresented: Binding<Bool>,
        onDismiss: (() -> Void)? = nil,
        @ViewBuilder content: @escaping () -> Content
    ) -> some View {
        sheet(isPresented: isPresented, onDismiss: onDismiss, content: content)
    }
}

extension ToolbarItemPlacement {
    /// iOS 17's top-bar placements, mapped to the macOS toolbar equivalents.
    static var topBarLeading: ToolbarItemPlacement { .navigation }
    static var topBarTrailing: ToolbarItemPlacement { .primaryAction }
    /// macOS has no bottom toolbar; items land in the window toolbar instead.
    static var bottomBar: ToolbarItemPlacement { .automatic }
}

extension PickerStyle where Self == DefaultPickerStyle {
    /// `.navigationLink` pickers are an iOS navigation idiom; use the default
    /// (pop-up button) style on the Mac.
    static var navigationLink: DefaultPickerStyle { .automatic }
}

extension ListStyle where Self == InsetListStyle {
    /// `.insetGrouped` doesn't exist on macOS; `.inset` is the closest look.
    static var insetGrouped: InsetListStyle { .inset }
}

/// Mirror of `SearchFieldPlacement.NavigationBarDrawerDisplayMode` for
/// `.navigationBarDrawer(...)` call sites; the drawer is an iOS concept.
enum CompatNavigationBarDrawerDisplayMode {
    case automatic, always
}

extension SearchFieldPlacement {
    static func navigationBarDrawer(displayMode: CompatNavigationBarDrawerDisplayMode) -> SearchFieldPlacement {
        .automatic
    }
}

/// Mirror of `PageTabViewStyle.IndexDisplayMode` for `.page(...)` call sites.
enum CompatPageIndexDisplayMode {
    case automatic, always, never
}

extension TabViewStyle where Self == DefaultTabViewStyle {
    /// Paged tab views are an iOS-only concept; fall back to the default style.
    static func page(indexDisplayMode: CompatPageIndexDisplayMode) -> DefaultTabViewStyle {
        .automatic
    }
}
#endif
