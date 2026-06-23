import Foundation

enum AcceptLanguage {
    /// RFC-7231 Accept-Language built from the device's ordered preferred
    /// languages, e.g. "zh-Hant-HK,zh-Hans;q=0.9,en;q=0.8".
    static let headerValue: String = {
        let langs = Locale.preferredLanguages.prefix(6)
        guard !langs.isEmpty else { return "en" }
        return langs.enumerated().map { idx, code in
            let q = max(0.1, 1.0 - Double(idx) * 0.1)
            return idx == 0 ? code : "\(code);q=\(String(format: "%.1f", q))"
        }.joined(separator: ",")
    }()
}
