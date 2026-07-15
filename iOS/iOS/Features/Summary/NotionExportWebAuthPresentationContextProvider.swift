import AuthenticationServices
import BeautifulMermaid
import Kingfisher
import MarkdownUI
import QuickLook
import SwiftUI
import TipKit
import os

@MainActor
final class NotionExportWebAuthPresentationContextProvider: NSObject, ASWebAuthenticationPresentationContextProviding {
    func presentationAnchor(for session: ASWebAuthenticationSession) -> ASPresentationAnchor {
        let scene = UIApplication.shared.connectedScenes
            .compactMap { $0 as? UIWindowScene }
            .first { $0.activationState == .foregroundActive }
        return scene?.keyWindow ?? ASPresentationAnchor()
    }
}

