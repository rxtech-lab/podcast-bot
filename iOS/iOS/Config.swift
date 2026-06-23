//
//  Config.swift
//  iOS
//
//  Created by Qiwei Li on 6/23/26.
//

import Foundation
import SwiftUI

/// Centralized brand strings. Use the `LocalizedStringKey` variants in SwiftUI
/// views (Text, Label, navigationTitle, etc.) and the `Raw` String variants in
/// code, interpolation, and APIs that take `String`.
enum AppStringLiteral {
    static let appTitleRaw = "PanelFM"
    static var appTitle: LocalizedStringKey { LocalizedStringKey(appTitleRaw) }

    static let stationTitleRaw = "Stations"
    static var stationTitle: LocalizedStringKey { LocalizedStringKey(stationTitleRaw) }

    static let stationNameRaw = "Stations"
    static var stationName: LocalizedStringKey { LocalizedStringKey(stationNameRaw) }

    static let stationsNameRaw = "Staions"
    static var stationsName: LocalizedStringKey { LocalizedStringKey(stationsNameRaw) }
}
