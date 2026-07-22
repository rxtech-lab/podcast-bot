//
//  ShareViewController.swift
//  iOSShareExtension
//
//  Created by Qiwei Li on 7/16/26.
//

import SwiftUI
#if canImport(UIKit)
import UIKit
#endif

final class ShareViewController: UIViewController {
    private var hostingController: UIHostingController<ShareExtensionRootView>?

    override func viewDidLoad() {
        super.viewDidLoad()
        let model = ShareExtensionModel(
            inputItems: extensionContext?.inputItems ?? [],
            openDiscussion: { [weak self] discussionID in
                guard let self,
                      let url = Self.discussionURL(id: discussionID) else { return false }
                return await self.openHostApp(url: url)
            },
            complete: { [weak self] in
                self?.extensionContext?.completeRequest(returningItems: nil)
            }
        )
        let hosting = UIHostingController(rootView: ShareExtensionRootView(model: model))
        hostingController = hosting
        addChild(hosting)
        hosting.view.translatesAutoresizingMaskIntoConstraints = false
        view.addSubview(hosting.view)
        NSLayoutConstraint.activate([
            hosting.view.leadingAnchor.constraint(equalTo: view.leadingAnchor),
            hosting.view.trailingAnchor.constraint(equalTo: view.trailingAnchor),
            hosting.view.topAnchor.constraint(equalTo: view.topAnchor),
            hosting.view.bottomAnchor.constraint(equalTo: view.bottomAnchor),
        ])
        hosting.didMove(toParent: self)
    }

    /// Share extensions are not one of the extension points for which iOS
    /// supports `NSExtensionContext.open`. Use the same responder-chain handoff
    /// as Linda's share extension so UIKit opens the containing app directly.
    private func openHostApp(url: URL) async -> Bool {
        await withCheckedContinuation { continuation in
            var responder: UIResponder? = self
            while let current = responder {
                if let application = current as? UIApplication {
                    application.open(url, options: [:]) { opened in
                        continuation.resume(returning: opened)
                    }
                    return
                }
                responder = current.next
            }
            continuation.resume(returning: false)
        }
    }

    private static func discussionURL(id: String) -> URL? {
        var components = URLComponents()
        components.scheme = "debatepod"
        components.host = "d"
        components.path = "/\(id)"
        return components.url
    }
}
