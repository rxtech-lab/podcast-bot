import AVKit
import Kingfisher
import MarkdownUI
import Photos
import PhotosUI
import RxAuthSwift
import SwiftUI
import TipKit
import UIKit
import UniformTypeIdentifiers
import os

enum SpeakerPalette {
    /// Distinct hues that all sit well on black and harmonize with the purple
    /// accent. The first entry is the accent itself so a lone host echoes the app.
    private static let colors: [Color] = [
        Theme.accent, // violet
        Color(red: 0.20, green: 0.72, blue: 0.90), // cyan
        Color(red: 0.95, green: 0.45, blue: 0.62), // rose
        Color(red: 0.97, green: 0.66, blue: 0.31), // amber
        Color(red: 0.36, green: 0.79, blue: 0.55), // green
        Color(red: 0.46, green: 0.56, blue: 0.98), // blue
    ]

    static func color(for speaker: String) -> Color {
        colors[index(for: speaker)]
    }

    static func color(for speaker: String, in lines: [LiveLine]) -> Color {
        colors[index(for: speaker, in: lines)]
    }

    static func index(for speaker: String) -> Int {
        guard !normalizedSpeaker(speaker).isEmpty else { return 0 }
        // djb2 — stable across launches so a speaker keeps the same color.
        var hash = 5381
        for scalar in speaker.unicodeScalars {
            hash = (hash &* 33) &+ Int(scalar.value)
        }
        return abs(hash) % colors.count
    }

    static func index(for speaker: String, in lines: [LiveLine]) -> Int {
        let target = normalizedSpeaker(speaker)
        guard !target.isEmpty else { return index(for: speaker) }
        var seen: [String: Int] = [:]
        var nextIndex = 0
        for line in lines {
            let key = normalizedSpeaker(line.speaker)
            guard !key.isEmpty, seen[key] == nil else { continue }
            seen[key] = nextIndex
            nextIndex += 1
        }
        return seen[target].map { $0 % colors.count } ?? index(for: speaker)
    }

    static func initials(for speaker: String) -> String {
        let letters = initialsSource(for: speaker)
            .split(separator: " ")
            .prefix(2)
            .compactMap(\.first)
            .map(String.init)
        return letters.isEmpty ? "?" : letters.joined().uppercased()
    }

    private static func initialsSource(for speaker: String) -> String {
        var result = ""
        var parenthesisDepth = 0
        for character in speaker {
            switch character {
            case "(", "（":
                parenthesisDepth += 1
            case ")", "）":
                if parenthesisDepth > 0 {
                    parenthesisDepth -= 1
                } else {
                    result.append(character)
                }
            default:
                if parenthesisDepth == 0 {
                    result.append(character)
                }
            }
        }
        return result.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    private static func normalizedSpeaker(_ speaker: String) -> String {
        speaker.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
    }
}

/// A small gradient avatar with the speaker's initials in their palette color.
