import Foundation

struct ShareOption: Identifiable, Hashable {
    let id: String
    let label: String
}

struct ShareDiscussionFormDefinition {
    let title: String
    let submitTitle: String
    let loadingTitle: String
    let topicTitle: String
    let topicDescription: String
    let types: [ShareOption]
    let templatesByType: [String: [ShareOption]]
    let languages: [ShareOption]
    let discussantsRange: ClosedRange<Int>
    let initialTopic: String
    let initialType: String
    let initialTemplate: String
    let initialLanguage: String
    let initialDiscussants: Int
    let initialGenerateCover: Bool
}

struct ShareAudioFormDefinition {
    let title: String
    let submitTitle: String
    let loadingTitle: String
    let maxSpeakersRange: ClosedRange<Int>
    let initialMaxSpeakers: Int
    let maxBytes: Int64
}

struct SharePrecheck {
    let discussion: ShareDiscussionFormDefinition
    let uploadAudio: ShareAudioFormDefinition?
}

struct SharePlanOption: Identifiable, Hashable {
    let id: String
    let title: String
    let topic: String
    let contentType: String

    var displayTitle: String {
        let trimmedTitle = title.trimmingCharacters(in: .whitespacesAndNewlines)
        if !trimmedTitle.isEmpty { return trimmedTitle }
        let trimmedTopic = topic.trimmingCharacters(in: .whitespacesAndNewlines)
        return trimmedTopic.isEmpty ? "Untitled Plan" : trimmedTopic
    }

    var isNews: Bool { contentType == "news" }
}

struct SharePlanPage {
    let plans: [SharePlanOption]
    let canLoadMore: Bool
}

struct ShareUploadResult {
    let filename: String
    let key: String?
    let markdown: String?
    let url: String?
    let mimeType: String

    var attachmentObject: [String: Any] {
        var object: [String: Any] = ["filename": filename, "mime_type": mimeType]
        if let key { object["key"] = key }
        if let markdown, !markdown.isEmpty { object["markdown"] = markdown }
        if let url, !url.isEmpty { object["url"] = url }
        return object
    }
}

enum ShareAPIError: LocalizedError {
    case signedOut
    case invalidResponse
    case server(Int, String)
    case missingUploadKey
    case couldNotOpenApp

    var errorDescription: String? {
        switch self {
        case .signedOut: "Open PanelFM and sign in before using the share extension."
        case .invalidResponse: "PanelFM received an invalid server response."
        case .server(_, let message): message.isEmpty ? "The server couldn't complete this request." : message
        case .missingUploadKey: "The upload did not return a storage key."
        case .couldNotOpenApp: "Your podcast was created, but PanelFM couldn't be opened. Open PanelFM to view it."
        }
    }
}

final class ShareExtensionAPI: @unchecked Sendable {
    private let baseURL: URL
    private let authIssuer: URL
    private let clientID: String
    private let keychain = SharedTokenKeychain()
    private let session: URLSession

    init(bundle: Bundle = .main, session: URLSession = .shared) {
        func configuredURL(_ key: String, fallback: String) -> URL {
            let raw = (bundle.object(forInfoDictionaryKey: key) as? String) ?? ""
            let normalized = raw.replacingOccurrences(of: "$(/)", with: "/")
            return URL(string: normalized.contains("$(") ? fallback : normalized) ?? URL(string: fallback)!
        }
        baseURL = configuredURL("AppAPIBaseURL", fallback: "https://server.podcast.rxlab.app")
        authIssuer = configuredURL("AppAuthIssuer", fallback: "https://auth.rxlab.app")
        clientID = (bundle.object(forInfoDictionaryKey: "AppAuthClientID") as? String) ?? ""
        self.session = session
    }

    func precheck() async throws -> SharePrecheck {
        let raw = try await request("GET", path: "/api/precheck?surface=share-extension")
        guard let newDiscussion = raw.dictionary("new_discussion")?.dictionary("form") else {
            throw ShareAPIError.invalidResponse
        }
        return SharePrecheck(
            discussion: try parseDiscussionForm(newDiscussion),
            uploadAudio: try raw.dictionary("upload_audio")?.dictionary("form").map(parseAudioForm)
        )
    }

    func plans(limit: Int, offset: Int, query: String) async throws -> SharePlanPage {
        var components = URLComponents()
        components.queryItems = [
            URLQueryItem(name: "limit", value: String(limit)),
            URLQueryItem(name: "offset", value: String(offset)),
        ]
        let trimmedQuery = query.trimmingCharacters(in: .whitespacesAndNewlines)
        if !trimmedQuery.isEmpty {
            components.queryItems?.append(URLQueryItem(name: "q", value: trimmedQuery))
        }
        let queryString = components.percentEncodedQuery.map { "?\($0)" } ?? ""
        let data = try await requestData("GET", path: "/api/discussions/plans\(queryString)")
        guard let rows = try JSONSerialization.jsonObject(with: data) as? [[String: Any]] else {
            throw ShareAPIError.invalidResponse
        }
        let plans: [SharePlanOption] = rows.compactMap { row -> SharePlanOption? in
            guard row["status"] as? String == "planning",
                  let id = row["id"] as? String, !id.isEmpty else { return nil }
            let script = row["script"] as? [String: Any]
            let contentType = row["content_type"] as? String ?? script?["type"] as? String ?? "discussion"
            guard contentType == "discussion" || contentType == "news" else { return nil }
            let rowTitle = row["title"] as? String ?? ""
            let scriptTitle = script?["title"] as? String ?? ""
            let title = rowTitle.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? scriptTitle : rowTitle
            return SharePlanOption(
                id: id,
                title: title,
                topic: row["topic"] as? String ?? "",
                contentType: contentType
            )
        }
        return SharePlanPage(plans: plans, canLoadMore: rows.count == limit)
    }

    func upload(item: IncomingShareItem, podcastAudio: Bool) async throws -> ShareUploadResult {
        guard let fileURL = item.fileURL else { throw ShareAPIError.invalidResponse }
        var presignBody: [String: Any] = [
            "filename": item.filename,
            "mime_type": item.mimeType,
        ]
        if podcastAudio { presignBody["kind"] = "podcast-audio" }
        let presign = try await request("POST", path: "/api/uploads/presign", body: presignBody)
        guard let rawUploadURL = presign["upload_url"] as? String,
              let uploadURL = URL(string: rawUploadURL),
              let key = presign["key"] as? String else { throw ShareAPIError.invalidResponse }

        var uploadRequest = URLRequest(url: uploadURL)
        uploadRequest.httpMethod = (presign["method"] as? String) ?? "PUT"
        if let headers = presign["headers"] as? [String: String] {
            for (name, value) in headers { uploadRequest.setValue(value, forHTTPHeaderField: name) }
        }
        let (uploadData, uploadResponse) = try await session.upload(for: uploadRequest, fromFile: fileURL)
        guard let http = uploadResponse as? HTTPURLResponse, (200..<300).contains(http.statusCode) else {
            throw ShareAPIError.server((uploadResponse as? HTTPURLResponse)?.statusCode ?? 0,
                                       String(decoding: uploadData, as: UTF8.self))
        }

        var completeBody: [String: Any] = [
            "key": key,
            "filename": item.filename,
            "mime_type": item.mimeType,
        ]
        if podcastAudio { completeBody["kind"] = "podcast-audio" }
        let complete = try await request("POST", path: "/api/uploads/complete", body: completeBody)
        return ShareUploadResult(
            filename: (complete["filename"] as? String) ?? item.filename,
            key: complete["key"] as? String,
            markdown: complete["markdown"] as? String,
            url: complete["url"] as? String,
            mimeType: (complete["mime_type"] as? String) ?? item.mimeType
        )
    }

    func createDiscussion(topic: String, type: String, template: String,
                          language: String, discussants: Int, generateCover: Bool,
                          attachments: [[String: Any]]) async throws -> String {
        let form: [String: Any] = [
            "prompt": ["topic": topic],
            "reference": ["discussion_id": ""],
            "settings": [
                "type": type,
                "template": template,
                "discussants": discussants,
                "language": language,
                "generate_cover": generateCover,
            ],
        ]
        let response = try await request("POST", path: "/api/discussions", body: [
            "form": form,
            "attachments": attachments,
        ])
        return try discussionID(from: response)
    }

    func createUploadedAudio(upload: ShareUploadResult, sizeBytes: Int64,
                             maxSpeakers: Int) async throws -> String {
        guard let key = upload.key, !key.isEmpty else { throw ShareAPIError.missingUploadKey }
        let response = try await request("POST", path: "/api/discussions/upload-audio", body: [
            "form": [
                "audio": [
                    "key": key,
                    "filename": upload.filename,
                    "mime_type": upload.mimeType,
                    "size_bytes": sizeBytes,
                ],
                "settings": ["max_speakers": maxSpeakers],
            ],
        ])
        return try discussionID(from: response)
    }

    func sendToPlan(id: String, prompt: String, attachments: [[String: Any]]) async throws {
        _ = try await requestData(
            "POST",
            path: "/api/discussions/\(id)/planning/messages",
            body: [
                "prompt": prompt,
                "attachments": attachments,
            ]
        )
    }

    private func discussionID(from response: [String: Any]) throws -> String {
        guard let id = response["id"] as? String, !id.isEmpty else {
            throw ShareAPIError.invalidResponse
        }
        return id
    }

    private func request(_ method: String, path: String,
                         body: [String: Any]? = nil, retrying: Bool = false) async throws -> [String: Any] {
        let data = try await requestData(method, path: path, body: body, retrying: retrying)
        guard let object = try JSONSerialization.jsonObject(with: data) as? [String: Any] else {
            throw ShareAPIError.invalidResponse
        }
        return object
    }

    private func requestData(_ method: String, path: String,
                             body: [String: Any]? = nil, retrying: Bool = false) async throws -> Data {
        guard let token = keychain.value(account: .accessToken), !token.isEmpty else {
            throw ShareAPIError.signedOut
        }
        guard let url = URL(string: path, relativeTo: baseURL) else { throw ShareAPIError.invalidResponse }
        var request = URLRequest(url: url)
        request.httpMethod = method
        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        request.setValue("ios-share-extension", forHTTPHeaderField: "X-Client-Platform")
        request.setValue(Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "",
                         forHTTPHeaderField: "X-Client-Version")
        request.setValue(Bundle.main.object(forInfoDictionaryKey: "CFBundleVersion") as? String ?? "",
                         forHTTPHeaderField: "X-Client-Build")
        if let body {
            request.httpBody = try JSONSerialization.data(withJSONObject: body)
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        }
        let (data, response) = try await session.data(for: request)
        guard let http = response as? HTTPURLResponse else { throw ShareAPIError.invalidResponse }
        if http.statusCode == 401, !retrying, try await refreshAccessToken() {
            return try await self.requestData(method, path: path, body: body, retrying: true)
        }
        guard (200..<300).contains(http.statusCode) else {
            throw ShareAPIError.server(http.statusCode, String(decoding: data, as: UTF8.self))
        }
        return data
    }

    private func refreshAccessToken() async throws -> Bool {
        guard let refreshToken = keychain.value(account: .refreshToken),
              !refreshToken.isEmpty, !clientID.isEmpty else { return false }
        let url = authIssuer.appendingPathComponent("api/oauth/token")
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.setValue("application/x-www-form-urlencoded", forHTTPHeaderField: "Content-Type")
        let fields = [
            "grant_type": "refresh_token",
            "refresh_token": refreshToken,
            "client_id": clientID,
        ]
        request.httpBody = fields.map { key, value in
            "\(formEncode(key))=\(formEncode(value))"
        }.joined(separator: "&").data(using: .utf8)
        let (data, response) = try await session.data(for: request)
        guard let http = response as? HTTPURLResponse, (200..<300).contains(http.statusCode),
              let object = try JSONSerialization.jsonObject(with: data) as? [String: Any],
              let accessToken = object["access_token"] as? String else { return false }
        try keychain.save(accessToken, account: .accessToken)
        if let newRefresh = object["refresh_token"] as? String {
            try keychain.save(newRefresh, account: .refreshToken)
        }
        let expiresIn = (object["expires_in"] as? NSNumber)?.doubleValue ?? 3600
        try keychain.save(String(Date().addingTimeInterval(expiresIn).timeIntervalSince1970), account: .expiresAt)
        return true
    }

    private func formEncode(_ value: String) -> String {
        value.addingPercentEncoding(withAllowedCharacters: .alphanumerics) ?? value
    }

    private func parseDiscussionForm(_ form: [String: Any]) throws -> ShareDiscussionFormDefinition {
        guard let schema = form.dictionary("schema"),
              let properties = schema.dictionary("properties"),
              let prompt = properties.dictionary("prompt")?.dictionary("properties")?.dictionary("topic"),
              let settings = properties.dictionary("settings"),
              let settingProperties = settings.dictionary("properties"),
              let initial = form.dictionary("initial_data"),
              let initialSettings = initial.dictionary("settings") else { throw ShareAPIError.invalidResponse }

        let types = parseOptions(settingProperties.dictionary("type")?["x-options"])
        let languages = parseOptions(settingProperties.dictionary("language")?["x-options"])
        let template = settingProperties.dictionary("template") ?? [:]
        let templateGroups = template["x-options-by-type"] as? [String: Any] ?? [:]
        var templatesByType: [String: [ShareOption]] = [:]
        for (type, raw) in templateGroups { templatesByType[type] = parseOptions(raw) }
        if templatesByType.isEmpty {
            let fallback = parseOptions(template["x-options"])
            for type in types { templatesByType[type.id] = fallback }
        }

        let discussants = settings.dictionary("then")?.dictionary("properties")?.dictionary("discussants")
        let minimum = (discussants?["minimum"] as? NSNumber)?.intValue ?? 2
        let maximum = (discussants?["maximum"] as? NSNumber)?.intValue ?? 6
        return ShareDiscussionFormDefinition(
            title: form["title"] as? String ?? "New Station",
            submitTitle: form["submit_title"] as? String ?? "Plan",
            loadingTitle: form["loading_title"] as? String ?? "Creating station…",
            topicTitle: prompt["title"] as? String ?? "Topic",
            topicDescription: prompt["description"] as? String ?? "",
            types: types,
            templatesByType: templatesByType,
            languages: languages,
            discussantsRange: minimum...max(maximum, minimum),
            initialTopic: initial.dictionary("prompt")?["topic"] as? String ?? "",
            initialType: initialSettings["type"] as? String ?? types.first?.id ?? "discussion",
            initialTemplate: initialSettings["template"] as? String ?? "default",
            initialLanguage: initialSettings["language"] as? String ?? languages.first?.id ?? "en-US",
            initialDiscussants: (initialSettings["discussants"] as? NSNumber)?.intValue ?? 3,
            initialGenerateCover: initialSettings["generate_cover"] as? Bool ?? false
        )
    }

    private func parseAudioForm(_ form: [String: Any]) throws -> ShareAudioFormDefinition {
        guard let schema = form.dictionary("schema"),
              let settings = schema.dictionary("properties")?.dictionary("settings"),
              let maxSpeakers = settings.dictionary("properties")?.dictionary("max_speakers") else {
            throw ShareAPIError.invalidResponse
        }
        let minimum = (maxSpeakers["minimum"] as? NSNumber)?.intValue ?? 2
        let maximum = (maxSpeakers["maximum"] as? NSNumber)?.intValue ?? 35
        let initial = form.dictionary("initial_data")?.dictionary("settings")
        let uiAudio = form.dictionary("ui_schema")?.dictionary("audio")?.dictionary("ui:options")
        return ShareAudioFormDefinition(
            title: form["title"] as? String ?? "Upload Own Audio",
            submitTitle: form["submit_title"] as? String ?? "Transcribe",
            loadingTitle: form["loading_title"] as? String ?? "Starting transcription…",
            maxSpeakersRange: minimum...max(maximum, minimum),
            initialMaxSpeakers: (initial?["max_speakers"] as? NSNumber)?.intValue ?? 2,
            maxBytes: (uiAudio?["max_bytes"] as? NSNumber)?.int64Value ?? 0
        )
    }

    private func parseOptions(_ raw: Any?) -> [ShareOption] {
        (raw as? [[String: Any]] ?? []).compactMap { value in
            guard let id = value["id"] as? String else { return nil }
            return ShareOption(id: id, label: value["label"] as? String ?? id)
        }
    }
}

private extension Dictionary where Key == String, Value == Any {
    func dictionary(_ key: String) -> [String: Any]? { self[key] as? [String: Any] }
}
