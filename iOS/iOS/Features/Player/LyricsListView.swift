import Kingfisher
import SwiftUI
import TipKit
#if canImport(UIKit)
import UIKit
#endif

struct LyricsListView: View {
    @Bindable var model: PlayerModel
    let foregroundPalette: FullScreenForegroundPalette
    private let tapSeekOffset = 0.05

    var body: some View {
        ScrollViewReader { proxy in
            let groups = model.lyricCueGroups
            ScrollView(showsIndicators: false) {
                LazyVStack(alignment: .leading, spacing: 18) {
                    ForEach(groups) { group in
                        LyricLine(
                            group: group,
                            speaker: model.speaker(for: group),
                            isActive: group.id == model.activeLyricGroupID,
                            foregroundPalette: foregroundPalette
                        )
                        .id(group.id)
                        .onTapGesture { model.seek(to: group.start + tapSeekOffset) }
                    }
                }
                .padding(.vertical, 40)
            }
            .onChange(of: model.activeLyricGroupID) { _, id in
                guard let id else { return }
                withAnimation(.spring(duration: 0.4)) {
                    proxy.scrollTo(id, anchor: .center)
                }
            }
            .onAppear {
                if let id = model.activeLyricGroupID {
                    proxy.scrollTo(id, anchor: .center)
                }
            }
        }
    }
}


