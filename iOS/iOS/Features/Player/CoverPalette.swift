import SwiftUI
#if canImport(UIKit)
import UIKit
#else
import AppKit
#endif

/// Extracts the prominent colors from a cover image so the full-screen player
/// can tint its background to match the artwork (Apple-Music style).
enum CoverPalette {
    /// A single bucketed color plus the pixel count and averaged RGB that
    /// produced it.
    private struct Candidate {
        var r: Double
        var g: Double
        var b: Double
        var count: Int
    }

    /// Returns up to `count` visually distinct prominent colors from image
    /// `data`, ordered by prominence. Downsamples to a small grid and buckets
    /// pixels by coarse RGB, then averages each bucket. Pure/CPU work — call it
    /// off the main actor.
    static func dominantColors(from data: Data, count: Int = 2) -> [Color] {
        #if canImport(UIKit)
        guard let cgImage = UIImage(data: data)?.cgImage else { return [] }
        #else
        guard let cgImage = NSImage(data: data)?
            .cgImage(forProposedRect: nil, context: nil, hints: nil) else { return [] }
        #endif

        let dimension = 40
        let bytesPerPixel = 4
        let bytesPerRow = bytesPerPixel * dimension
        var pixels = [UInt8](repeating: 0, count: dimension * dimension * bytesPerPixel)
        guard let context = CGContext(
            data: &pixels,
            width: dimension,
            height: dimension,
            bitsPerComponent: 8,
            bytesPerRow: bytesPerRow,
            space: CGColorSpaceCreateDeviceRGB(),
            bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue
        ) else { return [] }
        context.draw(cgImage, in: CGRect(x: 0, y: 0, width: dimension, height: dimension))

        // Bucket by 3 bits per channel (8 levels) and average within each bucket.
        var buckets: [Int: Candidate] = [:]
        for i in stride(from: 0, to: pixels.count, by: bytesPerPixel) {
            let alpha = pixels[i + 3]
            if alpha < 32 { continue }
            let r = Int(pixels[i])
            let g = Int(pixels[i + 1])
            let b = Int(pixels[i + 2])
            let key = ((r >> 5) << 6) | ((g >> 5) << 3) | (b >> 5)
            var c = buckets[key] ?? Candidate(r: 0, g: 0, b: 0, count: 0)
            c.r += Double(r); c.g += Double(g); c.b += Double(b); c.count += 1
            buckets[key] = c
        }

        let ranked = buckets.values
            .map { Candidate(r: $0.r / Double($0.count), g: $0.g / Double($0.count), b: $0.b / Double($0.count), count: $0.count) }
            .sorted { $0.count > $1.count }

        // Pick the most prominent buckets, skipping ones too close to an
        // already-chosen color so the two colors actually differ.
        var chosen: [Candidate] = []
        let minDistance = 48.0
        for cand in ranked {
            let distinct = chosen.allSatisfy { rgbDistance($0, cand) > minDistance }
            if distinct { chosen.append(cand) }
            if chosen.count >= count { break }
        }
        // If everything collapsed into one bucket, pad with the top color so the
        // caller still gets a usable pair.
        if chosen.isEmpty, let first = ranked.first { chosen = [first] }

        return chosen.map { Color(red: $0.r / 255, green: $0.g / 255, blue: $0.b / 255) }
    }

    private static func rgbDistance(_ a: Candidate, _ b: Candidate) -> Double {
        let dr = a.r - b.r, dg = a.g - b.g, db = a.b - b.b
        return (dr * dr + dg * dg + db * db).squareRoot()
    }
}
