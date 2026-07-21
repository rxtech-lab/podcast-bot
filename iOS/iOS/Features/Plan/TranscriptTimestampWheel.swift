import AVFoundation
import Observation
import SwiftUI

struct TranscriptTimestampWheel: View {
    @Binding var milliseconds: Int64
    let maximumMs: Int64

    private var showsHours: Bool {
        max(maximumMs, milliseconds) >= 3_600_000
    }

    var body: some View {
        HStack(spacing: 0) {
            if showsHours {
                wheel(
                    title: String(localized: "Hours", comment: "Hours in transcript timestamp wheel"),
                    values: Array(0...maximumHours),
                    selection: componentBinding(unitMs: 3_600_000, modulus: nil),
                    label: { String(format: "%02d", $0) }
                )
            }
            wheel(
                title: String(localized: "Minutes", comment: "Minutes in transcript timestamp wheel"),
                values: Array(0...59),
                selection: componentBinding(unitMs: 60_000, modulus: 60),
                label: { String(format: "%02d", $0) }
            )
            wheel(
                title: String(localized: "Seconds", comment: "Seconds in transcript timestamp wheel"),
                values: Array(0...59),
                selection: componentBinding(unitMs: 1_000, modulus: 60),
                label: { String(format: "%02d", $0) }
            )
            wheel(
                title: String(localized: "Milliseconds", comment: "Milliseconds in transcript timestamp wheel"),
                values: Array(0...999),
                selection: componentBinding(unitMs: 1, modulus: 1_000),
                label: { String(format: "%03d", $0) }
            )
        }
        .frame(height: 130)
        .accessibilityElement(children: .contain)
    }

    private var maximumHours: Int {
        max(Int(max(maximumMs, milliseconds) / 3_600_000), 0)
    }

    private func componentBinding(unitMs: Int64, modulus: Int?) -> Binding<Int> {
        Binding(
            get: {
                let value = Int(max(milliseconds, 0) / unitMs)
                return modulus.map { value % $0 } ?? value
            },
            set: { newValue in
                let current = max(milliseconds, 0)
                let oldComponent = Int(current / unitMs)
                let normalizedOld = modulus.map { oldComponent % $0 } ?? oldComponent
                milliseconds = max(current + Int64(newValue - normalizedOld) * unitMs, 0)
            }
        )
    }

    private func wheel(
        title: String,
        values: [Int],
        selection: Binding<Int>,
        label: @escaping (Int) -> String
    ) -> some View {
        Picker(title, selection: selection) {
            ForEach(values, id: \.self) { value in
                Text(label(value)).tag(value)
            }
        }
        #if os(macOS)
        .pickerStyle(.menu)
        #else
        .pickerStyle(.wheel)
        #endif
        .labelsHidden()
        .accessibilityLabel(title)
        .frame(maxWidth: .infinity)
        .clipped()
    }
}
