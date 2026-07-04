import ImageIO
import PhotosUI
import SwiftUI
import TipKit
import UniformTypeIdentifiers

/// Reusable cover-art picker — AI generate / upload / gradient — with a live
/// preview. Renders as a set of `Form` sections, so hosts embed it inside their
/// own `Form`. It binds the chosen `cover` and a shared `isWorking` flag so the
/// host can gate its confirm button while generation/upload is in flight.
///
/// Used by `PublishStationSheet` (publish flow) and `CoverEditorSheet` (set a
/// cover on any discussion without publishing).
struct CoverEditor: View {
    @Environment(AuthManager.self) private var auth
    @Environment(PurchaseManager.self) private var purchases

    let target: CoverGenerationTarget
    let title: String
    @Binding var cover: DiscussionCover
    @Binding var isWorking: Bool

    @State private var coverMode: CoverMode
    @State private var prompt: String
    @State private var selectedPhoto: PhotosPickerItem?
    @State private var showingImporter = false
    @State private var errorMessage: String?
    @State private var showingPaywall = false

    private let gradients = [
        ("#8E5CF7", "#00A3FF"),
        ("#FF5A7A", "#FFB000"),
        ("#00A86B", "#0066FF"),
        ("#202124", "#6A6EF6"),
    ]

    init(target: CoverGenerationTarget, title: String, cover: Binding<DiscussionCover>, isWorking: Binding<Bool>) {
        self.target = target
        self.title = title
        _cover = cover
        _isWorking = isWorking
        _prompt = State(initialValue: title)
        _coverMode = State(initialValue: CoverMode(cover: cover.wrappedValue))
    }

    var body: some View {
        Group {
            Section {
                StationCoverArt(cover: cover, title: title)
                    .aspectRatio(1, contentMode: .fit)
                    .frame(maxWidth: 260)
                    .frame(maxWidth: .infinity)
                    .listRowBackground(Color.clear)
            }

            Section("Cover") {
                Picker("Cover", selection: $coverMode) {
                    Label("AI", systemImage: "sparkles").tag(CoverMode.ai)
                    Label("Upload", systemImage: "photo").tag(CoverMode.upload)
                    Label("Gradient", systemImage: "swatchpalette").tag(CoverMode.gradient)
                }
                .pickerStyle(.segmented)

                switch coverMode {
                case .ai:
                    TextField("Image prompt", text: $prompt, axis: .vertical)
                        .lineLimit(2 ... 4)
                    Button {
                        generateCover()
                    } label: {
                        Label(isWorking ? "Generating" : "Generate Cover", systemImage: "sparkles")
                    }
                    .disabled(isWorking || prompt.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                    .popoverTip(GenerateCoverTip(), arrowEdge: .top)
                case .upload:
                    PhotosPicker(selection: $selectedPhoto, matching: .images) {
                        Label("Choose from Photos", systemImage: "photo.on.rectangle")
                    }
                    Button {
                        showingImporter = true
                    } label: {
                        Label("Choose File", systemImage: "folder")
                    }
                case .gradient:
                    LazyVGrid(columns: [GridItem(.adaptive(minimum: 68), spacing: 12)], spacing: 12) {
                        ForEach(gradients, id: \.0) { start, end in
                            Button {
                                cover = DiscussionCover(type: "gradient",
                                                        imageURL: nil,
                                                        imageKey: nil,
                                                        gradientStart: start,
                                                        gradientEnd: end,
                                                        prompt: nil)
                            } label: {
                                LinearGradient(colors: [Color(hex: start), Color(hex: end)],
                                               startPoint: .topLeading,
                                               endPoint: .bottomTrailing)
                                    .frame(height: 54)
                                    .clipShape(.rect(cornerRadius: 8))
                            }
                            .buttonStyle(.plain)
                        }
                    }
                }
            }
            .fileImporter(isPresented: $showingImporter,
                          allowedContentTypes: [.image],
                          allowsMultipleSelection: false) { result in
                if case .success(let urls) = result, let url = urls.first {
                    uploadFile(url)
                }
            }
            .onChange(of: selectedPhoto) { _, item in
                if let item { uploadPhoto(item) }
            }
            .sheet(isPresented: $showingPaywall) { PaywallScreen() }

            if let errorMessage {
                Section {
                    Text(errorMessage)
                        .font(.footnote)
                        .foregroundStyle(.red)
                }
            }
        }
    }

    private func generateCover() {
        isWorking = true
        errorMessage = nil
        Task { @MainActor in
            defer { isWorking = false }
            do {
                switch target {
                case .discussion(let id):
                    cover = try await APIClient(tokens: auth).generateDiscussionCover(id: id, prompt: prompt)
                case .album(let id):
                    cover = try await APIClient(tokens: auth).generateAlbumCover(id: id, prompt: prompt)
                }
                await purchases.refreshBalance()
            } catch let APIError.insufficientPoints(required, balance) {
                errorMessage = "You need \(UsageSummary.formatInt(required)) points but have \(UsageSummary.formatInt(balance))."
                await purchases.refreshBalance()
                showingPaywall = true
            } catch {
                report(error)
            }
        }
    }

    private func uploadPhoto(_ item: PhotosPickerItem) {
        let filename = "Cover-\(UUID().uuidString.prefix(6))"
        isWorking = true
        Task { @MainActor in
            defer {
                isWorking = false
                selectedPhoto = nil
            }
            do {
                guard let data = try await item.loadTransferable(type: Data.self) else { return }
                try await uploadImage(data: data, filename: filename)
            } catch {
                report(error)
            }
        }
    }

    private func uploadFile(_ url: URL) {
        let access = url.startAccessingSecurityScopedResource()
        let data = try? Data(contentsOf: url)
        if access { url.stopAccessingSecurityScopedResource() }
        guard let data else { return }
        isWorking = true
        Task { @MainActor in
            defer { isWorking = false }
            do {
                try await uploadImage(data: data, filename: url.lastPathComponent)
            } catch {
                report(error)
            }
        }
    }

    private func uploadImage(data: Data, filename: String) async throws {
        let payload = try imageUploadPayload(data: data, filename: filename)
        let resp = try await APIClient(tokens: auth).uploadFile(data: payload.data,
                                                                filename: payload.filename,
                                                                mimeType: payload.mimeType)
        guard resp.mimeType == payload.mimeType || resp.mimeType?.hasPrefix("image/") == true else {
            throw APIError.invalidRequest("Choose an image file.")
        }
        cover = DiscussionCover(type: "image",
                                imageURL: resp.url,
                                imageKey: resp.key,
                                gradientStart: nil,
                                gradientEnd: nil,
                                prompt: nil)
    }

    private func imageUploadPayload(data: Data, filename: String) throws -> CoverImageUploadPayload {
        guard let source = CGImageSourceCreateWithData(data as CFData, nil),
              let image = CGImageSourceCreateImageAtIndex(source, 0, nil) else {
            throw APIError.invalidRequest("Choose an image file.")
        }
        let base = uploadBaseName(filename)
        if let webp = encodedImagePayload(image: image,
                                          type: .webP,
                                          filename: "\(base).webp",
                                          mimeType: "image/webp",
                                          quality: 0.86) {
            return webp
        }
        if let jpeg = encodedImagePayload(image: image,
                                          type: .jpeg,
                                          filename: "\(base).jpg",
                                          mimeType: "image/jpeg",
                                          quality: 0.9) {
            return jpeg
        }
        throw APIError.invalidRequest("Could not convert image for upload.")
    }

    private func encodedImagePayload(image: CGImage,
                                     type: UTType,
                                     filename: String,
                                     mimeType: String,
                                     quality: CGFloat) -> CoverImageUploadPayload? {
        guard imageDestinationSupports(type) else { return nil }
        let output = NSMutableData()
        guard let destination = CGImageDestinationCreateWithData(output,
                                                                 type.identifier as CFString,
                                                                 1,
                                                                 nil) else { return nil }
        let options: [CFString: Any] = [
            kCGImageDestinationLossyCompressionQuality: quality,
        ]
        CGImageDestinationAddImage(destination, image, options as CFDictionary)
        guard CGImageDestinationFinalize(destination), output.length > 0 else { return nil }
        return CoverImageUploadPayload(data: output as Data,
                                       filename: filename,
                                       mimeType: mimeType)
    }

    private func imageDestinationSupports(_ type: UTType) -> Bool {
        let supported = CGImageDestinationCopyTypeIdentifiers() as NSArray
        return supported.contains(type.identifier)
    }

    private func uploadBaseName(_ filename: String) -> String {
        let base = ((filename as NSString).deletingPathExtension as NSString).lastPathComponent
        return base.isEmpty ? "Cover" : base
    }

    private func report(_ error: Error) {
        guard !APIClient.isCancellation(error) else { return }
        errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
    }
}

/// What the AI "Generate Cover" button generates art for — the server call
/// differs (and bills against) the discussion or the album.
enum CoverGenerationTarget {
    case discussion(id: String)
    case album(id: String)
}

/// The starting cover picker tab, derived from an existing cover's type.
enum CoverMode {
    case ai
    case upload
    case gradient

    init(cover: DiscussionCover) {
        switch cover.type {
        case "ai": self = .ai
        case "image": self = .upload
        default: self = .gradient
        }
    }
}

struct CoverImageUploadPayload {
    var data: Data
    var filename: String
    var mimeType: String
}

extension DiscussionCover {
    /// Default gradient cover used to seed the editor when a discussion has no
    /// usable cover yet.
    static var defaultGradient: DiscussionCover {
        DiscussionCover(type: "gradient",
                        imageURL: nil,
                        imageKey: nil,
                        gradientStart: "#8E5CF7",
                        gradientEnd: "#00A3FF",
                        prompt: nil)
    }
}
