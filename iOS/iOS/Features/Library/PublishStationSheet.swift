import PhotosUI
import SwiftUI
import UniformTypeIdentifiers

struct PublishStationSheet: View {
    @Environment(AuthManager.self) private var auth
    @Environment(PurchaseManager.self) private var purchases
    @Environment(\.dismiss) private var dismiss

    @Binding var discussion: Discussion
    @State private var coverMode: CoverMode = .gradient
    @State private var cover: DiscussionCover
    @State private var prompt: String
    @State private var selectedPhoto: PhotosPickerItem?
    @State private var showingImporter = false
    @State private var isWorking = false
    @State private var errorMessage: String?
    @State private var showingPaywall = false

    private let gradients = [
        ("#8E5CF7", "#00A3FF"),
        ("#FF5A7A", "#FFB000"),
        ("#00A86B", "#0066FF"),
        ("#202124", "#6A6EF6"),
    ]

    init(discussion: Binding<Discussion>) {
        _discussion = discussion
        let initialCover = discussion.wrappedValue.cover?.isPublishable == true
            ? discussion.wrappedValue.cover!
            : DiscussionCover(type: "gradient", imageURL: nil, imageKey: nil, gradientStart: "#8E5CF7", gradientEnd: "#00A3FF", prompt: nil)
        _cover = State(initialValue: initialCover)
        _prompt = State(initialValue: discussion.wrappedValue.displayTitle)
        _coverMode = State(initialValue: CoverMode(cover: initialCover))
    }

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    StationCoverArt(cover: cover, title: discussion.displayTitle)
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

                if let errorMessage {
                    Section {
                        Text(errorMessage)
                            .font(.footnote)
                            .foregroundStyle(.red)
                    }
                }
            }
            .navigationTitle(discussion.isPublic ? "Station Visibility" : "Publish Station")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button(discussion.isPublic ? "Update" : "Publish") {
                        publish()
                    }
                    .disabled(isWorking || !cover.isPublishable)
                }
            }
        }
        .presentationDetents([.large])
        .interactiveDismissDisabled(isWorking)
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
    }

    private func generateCover() {
        isWorking = true
        errorMessage = nil
        Task { @MainActor in
            defer { isWorking = false }
            do {
                cover = try await APIClient(tokens: auth).generateDiscussionCover(id: discussion.id, prompt: prompt)
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
        let utType = item.supportedContentTypes.first
        let ext = utType?.preferredFilenameExtension ?? "jpg"
        let mime = utType?.preferredMIMEType ?? "image/jpeg"
        let filename = "Cover-\(UUID().uuidString.prefix(6)).\(ext)"
        isWorking = true
        Task { @MainActor in
            defer {
                isWorking = false
                selectedPhoto = nil
            }
            do {
                guard let data = try await item.loadTransferable(type: Data.self) else { return }
                try await upload(data: data, filename: filename, mime: mime)
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
        let mime = UTType(filenameExtension: url.pathExtension)?.preferredMIMEType ?? "image/jpeg"
        isWorking = true
        Task { @MainActor in
            defer { isWorking = false }
            do {
                try await upload(data: data, filename: url.lastPathComponent, mime: mime)
            } catch {
                report(error)
            }
        }
    }

    private func upload(data: Data, filename: String, mime: String) async throws {
        let resp = try await APIClient(tokens: auth).uploadFile(data: data, filename: filename, mimeType: mime)
        guard resp.mimeType?.hasPrefix("image/") == true else {
            throw APIError.invalidRequest("Choose an image file.")
        }
        cover = DiscussionCover(type: "image",
                                imageURL: resp.url,
                                imageKey: resp.key,
                                gradientStart: nil,
                                gradientEnd: nil,
                                prompt: nil)
    }

    private func publish() {
        isWorking = true
        errorMessage = nil
        Task { @MainActor in
            defer { isWorking = false }
            do {
                discussion = try await APIClient(tokens: auth).updateDiscussionVisibility(
                    id: discussion.id,
                    visibility: .public,
                    cover: cover
                )
                dismiss()
            } catch {
                report(error)
            }
        }
    }

    private func report(_ error: Error) {
        guard !APIClient.isCancellation(error) else { return }
        errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
    }
}

private enum CoverMode {
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
