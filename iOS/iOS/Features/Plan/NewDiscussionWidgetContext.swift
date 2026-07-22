import JSONSchema
import JSONSchemaForm
import SwiftUI

/// Presentation helpers for backend-owned new-discussion form fields.
extension JSONSchemaFormWidgetContext {
    var fieldTitle: String { schema.title ?? propertyName ?? "" }
    var fieldDescription: String? { schema.description }

    private var options: [String: Any]? { uiSchema?["ui:options"] as? [String: Any] }

    /// SF Symbol name for the row's leading icon, supplied by the backend.
    var icon: String? { options?["icon"] as? String }

    /// Placeholder text for text inputs, supplied by the backend.
    var placeholder: String? { options?["placeholder"] as? String }

    /// Whether a text input grows vertically; defaults to the multi-line topic box.
    var isMultiline: Bool { options?["multiline"] as? Bool ?? true }

    /// Backend-declared accessibility identifier for UI tests.
    var accessibilityID: String? { options?["accessibility_id"] as? String }

    /// Backend-declared deep link used to open the parent-discussion picker.
    var deepLink: String? { options?["deep_link"] as? String }

    /// Enum options paired with their localized display labels (`ui:enumNames`).
    var enumOptions: [(value: String, label: String)] {
        guard let values = schema.enumSchema?.values else { return [] }
        let names = uiSchema?["ui:enumNames"] as? [String]
        return values.enumerated().compactMap { index, value in
            guard case .string(let raw) = value else { return nil }
            let label: String
            if let names, names.indices.contains(index), !names[index].isEmpty {
                label = names[index]
            } else {
                label = raw
            }
            return (raw, label)
        }
    }

    /// Type-scoped enum options supplied by the backend in `ui:options`.
    func enumOptions(forType type: String) -> [(value: String, label: String)]? {
        guard let groups = options?["options_by_type"] as? [String: Any],
              let rawOptions = groups[type] as? [Any] else {
            return nil
        }
        let parsed = rawOptions.compactMap { value -> (value: String, label: String)? in
            guard let option = value as? [String: Any],
                  let id = option["id"] as? String else {
                return nil
            }
            return (id, (option["label"] as? String) ?? id)
        }
        return parsed.isEmpty ? nil : parsed
    }

    var stringValue: Binding<String> {
        Binding(
            get: { self.formData.wrappedValue.string ?? "" },
            set: { self.formData.wrappedValue = .string($0) }
        )
    }

    var boolValue: Binding<Bool> {
        Binding(
            get: { self.formData.wrappedValue.boolean ?? false },
            set: { self.formData.wrappedValue = .boolean($0) }
        )
    }

    var intValue: Binding<Int> {
        Binding(
            get: { Int(self.formData.wrappedValue.number ?? 0) },
            set: { self.formData.wrappedValue = .number(Double($0)) }
        )
    }
}
