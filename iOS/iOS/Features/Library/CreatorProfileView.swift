import Kingfisher
import SwiftUI

struct CreatorProfileView: View {
    @Environment(AuthManager.self) private var auth
    @Environment(\.dismiss) private var dismiss
    let creatorID: String
    let initialProfile: CreatorProfile?
    let onCreateFromPlan: ((Discussion) -> Void)?

    @State private var profile: CreatorProfile?
    @State private var stations: [Discussion] = []
    @State private var path: [MarketDestination] = []
    @State private var isLoading = false
    @State private var isTogglingFollow = false
    @State private var errorMessage: String?

    private let pageSize = 20

    var body: some View {
        NavigationStack(path: $path) {
            ZStack {
                Theme.background.ignoresSafeArea()
                content
            }
            .navigationTitle(profile?.title ?? "Creator")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button { dismiss() } label: { Image(systemName: "xmark") }
                        .accessibilityLabel("Close")
                }
            }
            .navigationDestination(for: MarketDestination.self) { destination in
                switch destination {
                case .discussion(let discussion):
                    PodcastPlayerView(discussion: discussion,
                                      onCreatedFromPlan: onCreateFromPlan,
                                      onCreatedFollowUp: onCreateFromPlan,
                                      hidesTabBar: true)
                case .album(let id):
                    AlbumView(albumID: id, mode: .publicMarket) { episode in
                        path.append(.discussion(episode))
                    }
                }
            }
        }
        .task { await load() }
        .alert("Could not load creator", isPresented: Binding(
            get: { errorMessage != nil },
            set: { if !$0 { errorMessage = nil } }
        )) {
            Button("OK", role: .cancel) { errorMessage = nil }
        } message: {
            Text(errorMessage ?? "")
        }
    }

    @ViewBuilder
    private var content: some View {
        if isLoading && profile == nil {
            ProgressView()
                .tint(Theme.accent)
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        } else if let profile {
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 22) {
                    creatorHeader(profile)
                        .padding(.horizontal, 16)
                        .padding(.top, 14)
                    creatorStationsContent
                }
                .padding(.bottom, 28)
            }
            .refreshable { await load() }
        } else {
            ContentUnavailableView("Creator Not Found",
                                   systemImage: "person.crop.circle.badge.questionmark",
                                   description: Text("This creator profile is not available."))
        }
    }

    @ViewBuilder
    private var creatorStationsContent: some View {
        if isLoading && stations.isEmpty {
            ProgressView()
                .tint(Theme.accent)
                .frame(maxWidth: .infinity)
                .padding(.vertical, 44)
        } else if stations.isEmpty {
            ContentUnavailableView("No Public \(AppStringLiteral.stationsNameRaw)",
                                   systemImage: "music.note.list",
                                   description: Text("Public \(AppStringLiteral.stationsNameRaw) from this creator appear here."))
                .padding(.vertical, 28)
        } else {
            let items = marketDisplayItems(from: stations)
            VStack(alignment: .leading, spacing: 12) {
                Text("Public \(AppStringLiteral.stationsNameRaw)")
                    .font(.headline)
                    .padding(.horizontal, 16)
                LazyVGrid(columns: [GridItem(.adaptive(minimum: 150), spacing: 14)], spacing: 18) {
                    ForEach(items) { item in
                        MarketItemCard(
                            item: item,
                            onOpen: { path.append($0.destination) },
                            onToggleLike: toggleLike
                        )
                    }
                }
                .padding(.horizontal, 16)
            }
        }
    }

    private func creatorHeader(_ profile: CreatorProfile) -> some View {
        HStack(spacing: 16) {
            CreatorAvatar(profile: profile, size: 78)
            VStack(alignment: .leading, spacing: 6) {
                Text(profile.title)
                    .font(.title2.weight(.semibold))
                    .lineLimit(2)
                Text(profile.followerText)
                    .font(.subheadline)
                    .foregroundStyle(Theme.secondaryText)
            }
            Spacer(minLength: 0)
            if profile.isSelf != true {
                Button {
                    toggleFollow()
                } label: {
                    Text(profile.isFollowed == true ? "Following" : "Follow")
                        .font(.subheadline.weight(.semibold))
                }
                .buttonStyle(.borderedProminent)
                .disabled(isTogglingFollow)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .glassCard()
    }

    @MainActor
    private func load() async {
        isLoading = true
        defer { isLoading = false }
        do {
            let api = APIClient(tokens: auth)
            async let loadedProfile = api.creatorProfile(id: creatorID)
            async let loadedStations = api.creatorStations(id: creatorID, limit: pageSize)
            profile = try await loadedProfile
            stations = try await loadedStations
        } catch {
            guard !APIClient.isCancellation(error) else { return }
            profile = initialProfile
            report(error)
        }
    }

    private func toggleFollow() {
        guard let current = profile, current.isSelf != true else { return }
        isTogglingFollow = true
        Task { @MainActor in
            defer { isTogglingFollow = false }
            do {
                let api = APIClient(tokens: auth)
                profile = current.isFollowed == true
                    ? try await api.unfollowCreator(id: current.id)
                    : try await api.followCreator(id: current.id)
            } catch {
                report(error)
            }
        }
    }

    private func toggleLike(_ station: Discussion) {
        Task { @MainActor in
            do {
                let api = APIClient(tokens: auth)
                let updated = (station.isLiked == true)
                    ? try await api.unlikeMarketStation(id: station.id)
                    : try await api.likeMarketStation(id: station.id)
                if let index = stations.firstIndex(where: { $0.id == updated.id }) {
                    stations[index] = updated.preservingRenderableCover(from: station)
                }
            } catch {
                report(error)
            }
        }
    }

    @MainActor
    private func report(_ error: Error) {
        guard !APIClient.isCancellation(error) else { return }
        errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
    }
}


