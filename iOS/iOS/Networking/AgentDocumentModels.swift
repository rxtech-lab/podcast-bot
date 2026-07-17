import Foundation

struct AgentDocumentDTO: Codable, Hashable, Sendable, Identifiable {
    var id: String
    var title: String
    var markdown: String?
    var discussionID: String?
    var podcastTitle: String?
    var createdAt: String
    var updatedAt: String

    enum CodingKeys: String, CodingKey {
        case id, title, markdown
        case discussionID = "discussion_id"
        case podcastTitle = "podcast_title"
        case createdAt = "created_at"
        case updatedAt = "updated_at"
    }
}

struct AgentDocumentListResponse: Codable, Sendable {
    var documents: [AgentDocumentDTO]
    var hasMore: Bool?

    enum CodingKeys: String, CodingKey {
        case documents
        case hasMore = "has_more"
    }
}
