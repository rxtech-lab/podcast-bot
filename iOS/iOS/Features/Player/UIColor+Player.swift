#if canImport(UIKit)
import UIKit

typealias PlatformColor = UIColor
typealias PlatformImage = UIImage
#else
import AppKit

typealias PlatformColor = NSColor
typealias PlatformImage = NSImage
#endif

extension PlatformColor {
    convenience init(hex: String) {
        let clean = hex.trimmingCharacters(in: CharacterSet(charactersIn: "#")).uppercased()
        var value: UInt64 = 0
        Scanner(string: clean).scanHexInt64(&value)
        let red = CGFloat((value >> 16) & 0xFF) / 255.0
        let green = CGFloat((value >> 8) & 0xFF) / 255.0
        let blue = CGFloat(value & 0xFF) / 255.0
        self.init(red: red, green: green, blue: blue, alpha: 1)
    }
}
