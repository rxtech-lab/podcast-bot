struct SourceSearchRequest: Codable, Sendable {
    var query: String
}

struct SourceSearchResponse: Codable, Sendable {
    var sources: [SourceDTO]
}
