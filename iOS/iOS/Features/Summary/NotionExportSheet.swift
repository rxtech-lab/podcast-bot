import AuthenticationServices
import BeautifulMermaid
import Kingfisher
import MarkdownUI
import QuickLook
import SwiftUI
import TipKit
import os

struct NotionExportSheet: View {
    let api: APIClient
    let discussionID: String
    var docType: String = "summary"
    var language: String? = nil

    @Environment(\.dismiss) private var dismiss

    @State private var phase: Phase = .checking
    @State private var query = ""
    @State private var pages: [NotionPageDTO] = []
    @State private var selectedPageID: String?
    @State private var isExporting = false
    @State private var isConnecting = false
    @State private var errorMessage: String?
    @State private var createdURL: URL?
    @State private var authSession: ASWebAuthenticationSession?
    @State private var presentationProvider = NotionExportWebAuthPresentationContextProvider()

    private enum Phase: Equatable {
        case checking
        case needsConnect
        case picking
        case done
    }

    var body: some View {
        NavigationStack {
            content
                .navigationTitle("Export to Notion")
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    ToolbarItem(placement: .cancellationAction) {
                        Button(phase == .done ? "Done" : "Cancel") { dismiss() }
                    }
                    if phase == .picking {
                        ToolbarItemGroup(placement: .topBarTrailing) {
                            Button {
                                allowMorePages()
                            } label: {
                                Label("Allow Access to More Pages", systemImage: "folder.badge.plus")
                            }
                            .disabled(isConnecting || isExporting)
                        }
                        ToolbarItem(placement: .confirmationAction) {
                            Button(isExporting ? "Exporting…" : "Export") {
                                Task { await export() }
                            }
                            .disabled(isExporting)
                        }
                    }
                }
                .task { await loadStatus() }
        }
    }

    @ViewBuilder
    private var content: some View {
        switch phase {
        case .checking:
            ProgressView()
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        case .needsConnect:
            connectState
        case .picking:
            pickerList
        case .done:
            doneState
        }
    }

    private var connectState: some View {
        VStack(spacing: 16) {
            Image(systemName: "link.circle")
                .font(.largeTitle)
                .foregroundStyle(.secondary)
            Text("Connect your Notion workspace to export this summary.")
                .font(.callout)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
            if let errorMessage {
                Text(errorMessage)
                    .font(.footnote)
                    .foregroundStyle(.red)
                    .multilineTextAlignment(.center)
            }
            Button("Connect Notion") { connect() }
                .buttonStyle(.borderedProminent)
        }
        .padding()
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private var pickerList: some View {
        List {
            if let errorMessage {
                Text(errorMessage)
                    .font(.footnote)
                    .foregroundStyle(.red)
            }
            Section {
                ForEach(pages) { page in
                    Button { toggle(page) } label: { pageRow(page) }
                }
            } header: {
                Text("Choose a parent page")
            } footer: {
                Text("Select a page to export inside it, or leave it empty to export at the root.")
            }
        }
        .searchable(text: $query, prompt: "Search pages")
        .task(id: query) { await search() }
    }

    @ViewBuilder
    private func pageRow(_ page: NotionPageDTO) -> some View {
        let selected = selectedPageID == page.id
        HStack(spacing: 12) {
            Image(systemName: selected ? "checkmark.circle.fill" : "circle")
                .foregroundStyle(selected ? Color.accentColor : Color.secondary)
            VStack(alignment: .leading, spacing: 3) {
                Text(page.title.isEmpty ? "Untitled" : page.title)
                    .font(.body.weight(.medium))
                    .foregroundStyle(.primary)
                if let url = page.url, !url.isEmpty {
                    Text(url)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                }
            }
            Spacer()
        }
    }

    private func toggle(_ page: NotionPageDTO) {
        selectedPageID = (selectedPageID == page.id) ? nil : page.id
    }

    private var doneState: some View {
        VStack(spacing: 16) {
            Image(systemName: "checkmark.circle.fill")
                .font(.largeTitle)
                .foregroundStyle(.green)
            Text("Summary exported to Notion.")
                .font(.headline)
            if let createdURL {
                Link(destination: createdURL) {
                    Label("Open in Notion", systemImage: "arrow.up.right.square")
                }
                .buttonStyle(.borderedProminent)
            }
        }
        .padding()
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private func loadStatus() async {
        do {
            let status = try await api.notionStatus()
            if status.connected {
                phase = .picking
            } else {
                phase = .needsConnect
            }
        } catch {
            errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
            phase = .needsConnect
        }
    }

    private func search() async {
        let currentQuery = query
        errorMessage = nil
        try? await Task.sleep(for: .milliseconds(250))
        guard !Task.isCancelled else { return }
        do {
            let result = try await api.searchNotionPages(query: currentQuery)
            guard !Task.isCancelled, currentQuery == query else { return }
            pages = result
        } catch {
            guard !Task.isCancelled, currentQuery == query else { return }
            errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
        }
    }

    private func connect() {
        errorMessage = nil
        Task {
            do {
                let url = try await api.notionAuthURL()
                let session = ASWebAuthenticationSession(url: url, callbackURLScheme: "debatepod") { _, error in
                    Task { @MainActor in
                        authSession = nil
                        guard error == nil else { return }
                        await loadStatus()
                    }
                }
                session.presentationContextProvider = presentationProvider
                session.prefersEphemeralWebBrowserSession = false
                authSession = session
                session.start()
            } catch {
                errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
        }
    }

    private func export() async {
        await export(parentPageID: selectedPageID)
    }

    private func export(parentPageID: String?) async {
        guard !isExporting else { return }
        isExporting = true
        errorMessage = nil
        defer { isExporting = false }
        do {
            let resp = try await api.exportSummaryToNotion(id: discussionID,
                                                           parentPageID: parentPageID,
                                                           docType: docType,
                                                           language: language)
            createdURL = URL(string: resp.url)
            phase = .done
        } catch {
            errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
        }
    }

    private func allowMorePages() {
        guard !isConnecting else { return }
        isConnecting = true
        errorMessage = nil
        Task {
            do {
                let url = try await api.notionAuthURL()
                let session = ASWebAuthenticationSession(url: url, callbackURLScheme: "debatepod") { _, error in
                    Task { @MainActor in
                        authSession = nil
                        isConnecting = false
                        guard error == nil else { return }
                        if phase == .picking {
                            await search()
                        } else {
                            await loadStatus()
                        }
                    }
                }
                session.presentationContextProvider = presentationProvider
                session.prefersEphemeralWebBrowserSession = false
                authSession = session
                session.start()
            } catch {
                isConnecting = false
                errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
        }
    }
}
