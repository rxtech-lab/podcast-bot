import SwiftUI
import Charts

/// Dedicated points-usage history: a Swift Charts line of the balance over time
/// plus an itemized ledger. Reloads on appear, pull-to-refresh, and the reload
/// button. Presented as a sheet when the user taps their points balance.
struct PointsHistoryView: View {
    @Environment(AuthManager.self) private var auth
    @Environment(PurchaseManager.self) private var purchases
    @Environment(\.dismiss) private var dismiss

    var embedsInNavigationStack = true
    var showsCloseButton = true

    @State private var balance: Int?
    @State private var entries: [PointsLedgerEntry] = []
    @State private var isLoading = false
    @State private var isLoadingMore = false
    @State private var canLoadMore = false
    @State private var hasLoadedInitialPage = false
    @State private var errorMessage: String?
    @State private var showingPaywall = false
    private let pageSize = 50

    /// Oldest → newest, for a left-to-right chart.
    private var chronological: [PointsLedgerEntry] {
        entries.sorted { $0.createdAt < $1.createdAt }
    }

    var body: some View {
        if embedsInNavigationStack {
            NavigationStack {
                pointsScreen
            }
            #if os(macOS)
            .frame(minWidth: 620,
                   idealWidth: 720,
                   maxWidth: 820,
                   minHeight: 500,
                   idealHeight: 620,
                   maxHeight: 760)
            #endif
        } else {
            pointsScreen
        }
    }

    private var pointsScreen: some View {
        ZStack {
            Theme.background.ignoresSafeArea()
            content
        }
        .navigationTitle("Points")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            if showsCloseButton {
                #if os(macOS)
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }
                        .keyboardShortcut(.cancelAction)
                }
                #else
                ToolbarItem(placement: .topBarLeading) {
                    Button { dismiss() } label: { Image(systemName: "xmark") }
                        .accessibilityLabel("Close")
                }
                #endif
            }
            ToolbarItem(placement: .topBarTrailing) {
                Button { Task { await load() } } label: {
                    Image(systemName: "arrow.clockwise")
                }
                .disabled(isLoading || isLoadingMore)
                .accessibilityLabel("Reload history")
            }
        }
        .sheet(isPresented: $showingPaywall) { PaywallScreen() }
        .task {
            if !hasLoadedInitialPage {
                await load()
            }
        }
        .refreshable { await load() }
    }

    @ViewBuilder
    private var content: some View {
        ScrollView {
            LazyVStack(spacing: 20) {
                header

                if isLoading && !hasLoadedInitialPage {
                    loadingState
                } else {
                    if let errorMessage {
                        errorBanner(errorMessage)
                    }
                    if !chronological.isEmpty {
                        chartCard
                    }
                    if hasLoadedInitialPage && entries.isEmpty && errorMessage == nil {
                        emptyState
                    } else if !entries.isEmpty {
                        ledgerSection
                        loadMoreFooter
                    }
                }
            }
            .frame(maxWidth: 720)
            .padding(20)
            .frame(maxWidth: .infinity)
        }
    }

    private var header: some View {
        HStack(alignment: .center, spacing: 24) {
            VStack(alignment: .leading, spacing: 4) {
                Text("Balance")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(Theme.secondaryText)
                Text(UsageSummary.formatInt(balance ?? purchases.pointsBalance ?? 0))
                    .font(.largeTitle.weight(.bold))
                    .monospacedDigit()
                    .contentTransition(.numericText())
                Text("points remaining")
                    .font(.caption)
                    .foregroundStyle(Theme.secondaryText)
            }
            Spacer()
            getPointsButton
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        #if os(macOS)
        .padding(20)
        .background(Theme.rowBackground, in: .rect(cornerRadius: 18))
        .overlay {
            RoundedRectangle(cornerRadius: 18)
                .strokeBorder(Theme.divider.opacity(0.45))
        }
        #endif
    }

    private var getPointsButton: some View {
        Button { showingPaywall = true } label: {
            Label("Get Points", systemImage: "plus")
                .font(.subheadline.weight(.semibold))
        }
        #if os(macOS)
        .buttonStyle(.borderedProminent)
        .controlSize(.large)
        .tint(Theme.accent)
        #else
        .padding(.horizontal, 14)
        .padding(.vertical, 10)
        .glassEffect(in: .capsule)
        #endif
        .accessibilityIdentifier("points.getPoints")
    }

    private var loadingState: some View {
        VStack(spacing: 12) {
            ProgressView()
                .tint(Theme.accent)
            Text("Loading activity…")
                .font(.callout)
                .foregroundStyle(Theme.secondaryText)
        }
        .frame(maxWidth: .infinity, minHeight: 220)
    }

    private var emptyState: some View {
        ContentUnavailableView(
            "No usage yet",
            systemImage: "chart.line.uptrend.xyaxis",
            description: Text("Planning and generating \(AppStringLiteral.stationsNameRaw) will show up here.")
        )
        .frame(maxWidth: .infinity, minHeight: 240)
        #if os(macOS)
        .background(Theme.rowBackground.opacity(0.65), in: .rect(cornerRadius: 18))
        #endif
    }

    private func errorBanner(_ message: String) -> some View {
        Label(message, systemImage: "exclamationmark.triangle.fill")
            .font(.callout)
            .foregroundStyle(.red)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(14)
            .background(Color.red.opacity(0.08), in: .rect(cornerRadius: 12))
    }

    private var chartCard: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("Balance over time")
                .font(.caption.weight(.bold))
                .foregroundStyle(Theme.accent)
            Chart(chronological) { entry in
                LineMark(
                    x: .value("Time", entry.date),
                    y: .value("Balance", entry.balanceAfter)
                )
                .interpolationMethod(.monotone)
                .foregroundStyle(Theme.accent)

                AreaMark(
                    x: .value("Time", entry.date),
                    y: .value("Balance", entry.balanceAfter)
                )
                .interpolationMethod(.monotone)
                .foregroundStyle(
                    .linearGradient(
                        colors: [Theme.accent.opacity(0.25), Theme.accent.opacity(0.02)],
                        startPoint: .top, endPoint: .bottom
                    )
                )

                PointMark(
                    x: .value("Time", entry.date),
                    y: .value("Balance", entry.balanceAfter)
                )
                .foregroundStyle(Theme.accent)
                .symbolSize(18)
            }
            .chartYScale(domain: .automatic(includesZero: true))
            .frame(height: 200)
        }
        .padding(14)
        .background(Theme.agentBubble, in: .rect(cornerRadius: 16))
    }

    private var ledgerSection: some View {
        VStack(alignment: .leading, spacing: 0) {
            Text("Activity")
                .font(.caption.weight(.bold))
                .foregroundStyle(Theme.secondaryText)
                .padding(.bottom, 8)
            LazyVStack(spacing: 0) {
                ForEach(entries) { entry in
                    LedgerRow(entry: entry)
                    if entry.id != entries.last?.id {
                        Divider().overlay(Theme.secondaryText.opacity(0.15))
                    }
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    @ViewBuilder
    private var loadMoreFooter: some View {
        if isLoadingMore {
            ProgressView()
                .tint(Theme.accent)
                .padding(.vertical, 12)
        } else if canLoadMore && !entries.isEmpty {
            ProgressView()
                .tint(Theme.accent)
                .padding(.vertical, 12)
                .onAppear {
                    Task { await loadMore() }
                }
        }
    }

    private func load() async {
        guard !isLoading else { return }
        isLoading = true
        errorMessage = nil
        defer {
            isLoading = false
            hasLoadedInitialPage = true
        }
        do {
            let resp = try await APIClient(tokens: auth).pointsHistory(limit: pageSize, offset: 0)
            balance = resp.balance
            entries = resp.entries
            canLoadMore = resp.hasMore
            await purchases.refreshBalance()
        } catch {
            errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
        }
    }

    private func loadMore() async {
        guard canLoadMore, !isLoadingMore, !isLoading else { return }
        isLoadingMore = true
        defer { isLoadingMore = false }
        do {
            let resp = try await APIClient(tokens: auth).pointsHistory(limit: pageSize, offset: entries.count)
            balance = resp.balance
            let existing = Set(entries.map(\.id))
            entries.append(contentsOf: resp.entries.filter { !existing.contains($0.id) })
            canLoadMore = resp.hasMore
        } catch {
            errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
        }
    }
}

private struct LedgerRow: View {
    let entry: PointsLedgerEntry

    var body: some View {
        HStack(spacing: 12) {
            Image(systemName: icon)
                .font(.callout)
                .foregroundStyle(tint)
                .frame(width: 28, height: 28)
                .background(tint.opacity(0.12), in: .circle)
            VStack(alignment: .leading, spacing: 2) {
                Text(label)
                    .font(.callout.weight(.medium))
                Text(entry.date.formatted(date: .abbreviated, time: .shortened))
                    .font(.caption)
                    .foregroundStyle(Theme.secondaryText)
            }
            Spacer(minLength: 12)
            VStack(alignment: .trailing, spacing: 2) {
                Text(signed)
                    .font(.callout.weight(.semibold))
                    .monospacedDigit()
                    .foregroundStyle(tint)
                Text("\(UsageSummary.formatInt(entry.balanceAfter)) left")
                    .font(.caption2)
                    .foregroundStyle(Theme.secondaryText)
                    .monospacedDigit()
            }
        }
        .padding(.vertical, 10)
    }

    private var signed: String {
        let n = entry.delta
        let prefix = n > 0 ? "+" : ""
        return prefix + UsageSummary.formatInt(n)
    }

    private var tint: Color { entry.delta >= 0 ? .green : .red }

    private var icon: String {
        switch true {
        case entry.reason.hasPrefix("purchase"): return "creditcard.fill"
        case entry.reason == "signup_grant": return "gift.fill"
        case entry.reason.hasPrefix("refund"): return "arrow.uturn.backward"
        case entry.reason.hasPrefix("reserve:generation"), entry.reason == "generation": return "waveform"
        case entry.reason.hasPrefix("reserve:planning"), entry.reason == "planning": return "list.bullet.rectangle"
        default: return entry.delta >= 0 ? "plus.circle" : "minus.circle"
        }
    }

    private var label: String {
        if entry.reason.hasPrefix("purchase") { return "Purchase" }
        switch entry.reason {
        case "signup_grant": return "Welcome bonus"
        case "planning": return "Planning"
        case "generation": return AppStringLiteral.stationNameRaw
        case "reserve:planning": return "Planning (hold)"
        case "reserve:generation": return "\(AppStringLiteral.stationNameRaw) (hold)"
        default:
            if entry.reason.hasPrefix("refund") { return "Refund" }
            return entry.reason.replacingOccurrences(of: "_", with: " ").capitalized
        }
    }
}
