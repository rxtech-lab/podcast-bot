import SwiftUI
import TipKit

/// Q&A chat: ask questions about one finished podcast (transcript + sources)
/// or, in global scope, about the whole library. The agent retrieves content
/// with tools; show_podcast / show_transcript / show_sources render dedicated
/// cards inline. Mirrors PlanConversationView's streaming chat structure.
struct QAConversationView: View {
    @Environment(AuthManager.self) var auth
    @Environment(PurchaseManager.self) var purchases
    let scope: QAScope
    var language: String?
    var allowsClearingMessages = false
    /// Opens a podcast from a card (global scope); the host view decides how.
    var onOpenPodcast: ((String) -> Void)? = nil

    @State var parts: [QAPart] = []
    @State var input = ""
    @State var isStreaming = false
    @State var streamHasActivity = false
    @State var progressText: String?
    @State var errorMessage: String?
    @State var showingErrorAlert = false
    @State var errorOffersTopUp = false
    @State var showingPaywall = false
    @State var selectedToolPart: QAPart?
    @State var selectedTranscriptCard: QATranscriptCard?
    @State var selectedMindmapDocument: QADocumentCard?
    @State var selectedPPTDocument: QADocumentCard?
    @State var selectedAgentDocument: QAAgentDocumentCard?
    @State var showingClearMessagesConfirmation = false
    @State var isClearingMessages = false
    @State var clearMessagesError: String?
    @State var didStart = false
    @State var isLoadingHistory = false
    @State var listIsAtBottom = true
    @State var shouldScrollToInitialBottom = false
    @State var didRequestInitialBottomScroll = false
    @State var streamTask: Task<Void, Never>?
    @State var initialScrollTask: Task<Void, Never>?
    @FocusState var inputFocused: Bool

    var body: some View {
        ZStack {
            Theme.background.ignoresSafeArea()

            if isLoadingHistory && parts.isEmpty {
                ProgressView()
                    .tint(Theme.accent)
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else if parts.isEmpty && !isStreaming {
                emptyState
            } else {
                MessageList(
                    messages: rows,
                    isStreaming: isStreaming,
                    shouldScrollToBottom: shouldScrollToInitialBottom,
                    scrollToBottomAnimated: false,
                    isAtBottom: $listIsAtBottom
                ) { row in
                    rowView(row)
                        .padding(.horizontal, 16)
                        .padding(.vertical, 7)
                }
                .scrollDismissesKeyboard(.interactively)
                .contentMargins(.bottom, 96, for: .scrollContent)
            }

            VStack { Spacer(); inputBar }
        }
        .navigationTitle(title)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            if allowsClearingMessages {
                ToolbarItem(placement: .topBarTrailing) {
                    Button {
                        showingClearMessagesConfirmation = true
                    } label: {
                        if isClearingMessages {
                            ProgressView()
                        } else {
                            Label("Clear All Messages", systemImage: "trash")
                        }
                    }
                    .labelStyle(.iconOnly)
                    .disabled(parts.isEmpty || isLoadingHistory || isStreaming || isClearingMessages)
                    .accessibilityLabel("Clear All Messages")
                    .accessibilityIdentifier("qa.clear")
                }
            }
        }
        .sheet(item: $selectedToolPart) { part in
            PlanningToolDetailSheet(part: part.asPlanningPart)
        }
        .sheet(item: $selectedTranscriptCard) { card in
            QATranscriptDetailSheet(card: card)
        }
        .sheet(item: $selectedMindmapDocument) { document in
            MindmapView(discussionID: document.discussionID,
                        title: document.title,
                        isEditable: canEditMindmap,
                        language: language,
                        api: APIClient(tokens: auth))
        }
        .sheet(item: $selectedPPTDocument) { document in
            SummaryView(discussionID: document.discussionID,
                        title: document.title,
                        language: language,
                        initialDocType: "ppt",
                        api: APIClient(tokens: auth))
        }
        .sheet(item: $selectedAgentDocument) { document in
            NavigationStack {
                AgentDocumentView(documentID: document.id,
                                  initialTitle: document.title,
                                  api: APIClient(tokens: auth)) { discussionID in
                    selectedAgentDocument = nil
                    openPodcast(id: discussionID)
                }
                .toolbar {
                    ToolbarItem(placement: .cancellationAction) {
                        Button("Done") { selectedAgentDocument = nil }
                    }
                }
            }
        }
        .sheet(isPresented: $showingPaywall) { PaywallScreen() }
        .alert("Could not send the message", isPresented: errorAlertBinding) {
            if errorOffersTopUp {
                Button("Top Up") {
                    clearError()
                    showingPaywall = true
                }
            }
            Button("OK", role: .cancel) { clearError() }
        } message: {
            Text(errorMessage ?? "")
        }
        .alert("Could not clear messages", isPresented: Binding(
            get: { clearMessagesError != nil },
            set: { if !$0 { clearMessagesError = nil } }
        )) {
            Button("OK", role: .cancel) { clearMessagesError = nil }
        } message: {
            Text(clearMessagesError ?? "")
        }
        .confirmationDialog(
            "Clear all messages?",
            isPresented: $showingClearMessagesConfirmation,
            titleVisibility: .visible
        ) {
            Button("Clear All Messages", role: .destructive) {
                clearAllMessages()
            }
            .accessibilityIdentifier("qa.clear.confirm")
            Button("Cancel", role: .cancel) {}
        } message: {
            Text(clearMessagesConfirmationMessage)
        }
        .task { await purchases.refreshBalance() }
        .onAppear(perform: start)
        .onDisappear {
            streamTask?.cancel()
            initialScrollTask?.cancel()
            showingPaywall = false
        }
    }

    var title: String {
        switch scope {
        case let .podcast(d):
            return d.displayTitle.isEmpty
                ? String(localized: "Ask", comment: "Q&A chat title fallback for one podcast")
                : d.displayTitle
        case .global:
            return String(localized: "Ask Your Library", comment: "Global chat title over all podcasts")
        }
    }

    var clearMessagesConfirmationMessage: String {
        switch scope {
        case .podcast:
            return String(localized: "This permanently removes every message from this podcast chat.")
        case .global:
            return String(localized: "This permanently removes every message from your library chat.")
        }
    }

    // MARK: - Rows

    var rows: [QARow] {
        var r = visibleParts.map { QARow(id: $0.id, content: .part($0)) }
        if isStreaming {
            r.append(QARow(id: "qa-loading", content: .loading))
        }
        return r
    }

    var visibleParts: [QAPart] {
        guard isStreaming else { return parts }
        var visible = parts
        if let last = visible.last, last.isTransientRunningTool {
            visible.removeLast()
        }
        return visible
    }

    @ViewBuilder
    func rowView(_ row: QARow) -> some View {
        switch row.content {
        case .loading:
            loadingBubble
        case let .part(part):
            partView(part)
        }
    }

    @ViewBuilder
    func partView(_ part: QAPart) -> some View {
        if part.kind == "text" {
            textBubble(part)
        } else if part.kind == "summary" {
            summaryChip
        } else if let card = part.card {
            cardView(card, part: part)
        } else {
            HStack {
                PlanningToolCard(part: part.asPlanningPart) { selectedToolPart = part }
                Spacer(minLength: 34)
            }
            .planningCardAppear(delay: 0.02)
        }
    }

    @ViewBuilder
    func textBubble(_ part: QAPart) -> some View {
        let text = (part.text ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
        if part.role == "user" {
            if !text.isEmpty {
                HStack {
                    Spacer(minLength: 46)
                    Text(text)
                        .font(.body)
                        .foregroundStyle(.white)
                        .padding(.horizontal, 14)
                        .padding(.vertical, 11)
                        .background(Theme.accent, in: .rect(cornerRadius: 20))
                }
                .frame(maxWidth: .infinity, alignment: .trailing)
            }
        } else if !text.isEmpty {
            MarkdownText(part.text ?? "")
                .font(.body)
                .foregroundStyle(.primary)
                .padding(.vertical, 4)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
    }

    /// Older history was folded into a summary to keep the agent's context
    /// bounded; the full history stays visible above this chip.
    var summaryChip: some View {
        HStack(spacing: 8) {
            Image(systemName: "clock.arrow.circlepath")
                .font(.caption)
            Text(String(localized: "Earlier conversation summarized", comment: "Chip shown where old chat history was compacted into a summary"))
                .font(.caption)
        }
        .foregroundStyle(Theme.secondaryText)
        .padding(.horizontal, 12)
        .padding(.vertical, 6)
        .background(Theme.agentBubble.opacity(0.6), in: .capsule)
        .frame(maxWidth: .infinity, alignment: .center)
    }

    @ViewBuilder
    func cardView(_ card: QACard, part: QAPart) -> some View {
        HStack {
            switch card.kind {
            case "podcast":
                if let podcast = card.podcast {
                    QAPodcastCardView(podcast: podcast) {
                        openPodcast(id: podcast.id)
                    }
                }
            case "podcasts":
                if let podcasts = card.podcasts, !podcasts.isEmpty {
                    QAPodcastGridView(podcasts: podcasts) { podcastID in
                        openPodcast(id: podcastID)
                    }
                }
            case "podcast_highlights", "highlight_lines":
                if let highlights = card.highlights, !highlights.isEmpty {
                    QAPodcastHighlightsView(groups: highlights) { podcastID in
                        openPodcast(id: podcastID)
                    }
                }
            case "transcript":
                if let transcript = card.transcript {
                    QATranscriptCardView(transcript: transcript) {
                        selectedTranscriptCard = transcript
                    }
                }
            case "sources":
                if let sources = card.sources, !sources.isEmpty {
                    QASourcesCardView(sources: sources)
                }
            case "mindmap", "ppt":
                if let document = card.document {
                    QADocumentCardView(document: document, kind: card.kind) {
                        if card.kind == "mindmap" {
                            selectedMindmapDocument = document
                        } else {
                            selectedPPTDocument = document
                        }
                    }
                }
            case "document":
                if let document = card.agentDocument {
                    QAAgentDocumentCardView(document: document) {
                        selectedAgentDocument = document
                    }
                }
            default:
                PlanningToolCard(part: part.asPlanningPart) { selectedToolPart = part }
            }
            Spacer(minLength: 34)
        }
        .planningCardAppear(delay: 0.04)
    }

    var canEditMindmap: Bool {
        guard language == nil else { return false }
        if case let .podcast(discussion) = scope {
            return discussion.isOwner == true
        }
        return true // Global chat only searches the signed-in user's library.
    }

    var loadingBubble: some View {
        HStack {
            HStack(spacing: 10) {
                if !streamHasActivity {
                    Text(progressText ?? String(localized: "Thinking…", comment: "Default progress text while the chat agent works"))
                        .font(.callout)
                        .foregroundStyle(Theme.secondaryText)
                        .transition(.opacity.combined(with: .scale(scale: 0.96, anchor: .leading)))
                }
                PlanningTypingDots()
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 11)
            .background(Theme.agentBubble, in: .rect(cornerRadius: 20))
            .animation(.easeInOut(duration: 0.18), value: streamHasActivity)
            Spacer(minLength: 34)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .planningCardAppear()
    }

    var emptyState: some View {
        VStack(spacing: 14) {
            Image(systemName: "bubble.left.and.text.bubble.right")
                .font(.system(size: 44))
                .foregroundStyle(Theme.accent)
            Text(emptyTitle)
                .font(.title3.weight(.semibold))
            Text(emptyMessage)
                .font(.subheadline)
                .foregroundStyle(Theme.secondaryText)
                .multilineTextAlignment(.center)
        }
        .padding(40)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    var emptyTitle: String {
        switch scope {
        case .podcast:
            return String(localized: "Ask about this podcast", comment: "Q&A empty state title for one podcast")
        case .global:
            return String(localized: "Ask about your podcasts", comment: "Global chat empty state title")
        }
    }

    var emptyMessage: String {
        switch scope {
        case .podcast:
            return String(localized: "Ask anything about what was said, who said it, or the sources behind it.", comment: "Q&A empty state message for one podcast")
        case .global:
            return String(localized: "Search across every episode's content and sources — try “which episode talked about…”.", comment: "Global chat empty state message")
        }
    }

    // MARK: - Input bar

    var inputBar: some View {
        HStack(spacing: 10) {
            TextField(inputPrompt, text: $input, axis: .vertical)
                .lineLimit(1 ... 4)
                .textFieldStyle(.plain)
                .focused($inputFocused)
                .accessibilityIdentifier("qa.input")
            Button(action: send) {
                Image(systemName: isStreaming ? "ellipsis" : "arrow.up.circle.fill")
                    .font(.title2)
                    .foregroundStyle(Theme.accent)
            }
            .disabled(!canSend)
            .accessibilityIdentifier("qa.send")
        }
        .padding(12)
        .glassEffect(in: .capsule)
        .modifier(QAChatDocumentTipModifier(isGlobal: usesGlobalDocumentTip))
        .padding(16)
    }

    var usesGlobalDocumentTip: Bool {
        if case .global = scope { return true }
        return false
    }

    var inputPrompt: String {
        switch scope {
        case .podcast:
            return String(localized: "Ask about this podcast", comment: "Q&A input placeholder for one podcast")
        case .global:
            return String(localized: "Ask about your podcasts", comment: "Global chat input placeholder")
        }
    }

    var errorAlertBinding: Binding<Bool> {
        Binding(
            get: { showingErrorAlert },
            set: {
                showingErrorAlert = $0
                if !$0 { clearError() }
            }
        )
    }
}

private struct QAChatDocumentTipModifier: ViewModifier {
    let isGlobal: Bool

    @ViewBuilder
    func body(content: Content) -> some View {
        if isGlobal {
            content.popoverTip(GlobalChatDocumentTip(), arrowEdge: .bottom)
        } else {
            content.popoverTip(PodcastChatDocumentTip(), arrowEdge: .bottom)
        }
    }
}
