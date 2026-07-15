import Foundation

struct TranscriptSegmentEdit: Identifiable {
    let index: Int
    let segment: TranscriptSegmentDTO

    var id: Int { index }
}
