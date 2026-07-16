import Foundation

enum LibraryListRow: Identifiable {
    case discussion(Discussion)
    case album(summary: AlbumSummaryDTO, newest: Discussion, count: Int)

    var id: String {
        switch self {
        case .discussion(let d): return "discussion:\(d.id)"
        case .album(let summary, _, _): return "album:\(summary.id)"
        }
    }
}
