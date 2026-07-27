import SwiftUI

/// Global-chat surfaces for the library: the compact layout's Chat tab, the
/// regular layout's inspector column, and the toolbar toggle that drives it.
extension LibraryView {
    /// Compact-only. Global chat over every podcast in the library. Selecting
    /// Chat pushes the conversation in this tab's navigation stack; the pushed
    /// view hides the tab bar, so popping it restores the bar with the
    /// navigation animation. Regular layouts use `chatInspector` instead.
    var chatTab: some View {
        NavigationStack {
            Color.clear
                .navigationTitle("Home")
                .navigationDestination(isPresented: $showingGlobalChat) {
                    QAConversationView(scope: .global, allowsClearingMessages: true) { discussionID in
                        showingGlobalChat = false
                        Task {
                            if let discussion = try? await APIClient(tokens: auth).discussion(id: discussionID) {
                                upsert(discussion)
                                navigate(to: discussion)
                            }
                        }
                    }
                    #if !os(macOS)
                    .toolbar(.hidden, for: .tabBar)
                    #endif
                }
                .onChange(of: showingGlobalChat) { _, isPresented in
                    if !isPresented, selectedTab == .chat {
                        selectedTab = .home
                    }
                }
        }
    }

    /// The third column: global chat over the whole library. Opening a podcast
    /// from a chat card drives the detail column and leaves the chat in place,
    /// which is the point of the three-column layout.
    var chatInspector: some View {
        NavigationStack {
            // The inspector's content is built even while dismissed, and its
            // toolbar items surface in the detail column's bar — so Clear All
            // Messages only exists while the chat is actually on screen.
            QAConversationView(scope: .global, allowsClearingMessages: showingGlobalChat) { discussionID in
                Task {
                    if let discussion = try? await APIClient(tokens: auth).discussion(id: discussionID) {
                        upsert(discussion)
                        navigate(to: discussion)
                    }
                }
            }
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button {
                        showingGlobalChat = false
                    } label: {
                        Label("Hide Chat", systemImage: "sidebar.trailing")
                    }
                    .accessibilityLabel("Hide Chat")
                }
            }
        }
        .inspectorColumnWidth(min: 320, ideal: 400, max: 620)
    }

    /// Inspector toggle in the detail column's toolbar. Absent entirely when
    /// Chat is not available; present but gated behind the paywall prompt when
    /// the feature exists and the subscription does not grant it.
    @ToolbarContentBuilder
    var chatInspectorToolbar: some ToolbarContent {
        if homeChatAction != nil {
            ToolbarItem(placement: .topBarTrailing) {
                Button {
                    toggleChatInspector()
                } label: {
                    Label("Chat", systemImage: "bubble.left.and.text.bubble.right")
                }
                .accessibilityLabel("Chat")
                .accessibilityIdentifier("library.chat")
            }
        }
    }

    func toggleChatInspector() {
        guard homeChatAction?.enabled == true else {
            showingGlobalChat = false
            showingChatUpgradePrompt = true
            return
        }
        showingGlobalChat.toggle()
    }
}
