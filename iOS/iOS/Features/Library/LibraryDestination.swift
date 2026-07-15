import Foundation

/// A pushable destination in the library's navigation: an individual podcast
/// (plan or player, by status) or an album's episode list.
enum LibraryDestination: Hashable {
    case discussion(Discussion)
    case album(id: String)
}
