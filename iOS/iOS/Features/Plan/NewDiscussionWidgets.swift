import JSONSchema
import JSONSchemaForm
import SwiftUI

/// Custom `JSONSchemaForm` widgets + templates that render the backend-driven
/// New Discussion form with the original hand-built glass styling, while keeping
/// the form schema-driven and backend-owned.
///
/// The backend selects these by name via `ui:widget` / `ui:objectTemplate`, and
/// supplies per-row presentation (icons, deep links) via `ui:options`.
enum NewDiscussionFormUI {
    /// Glass leaf widgets keyed by the `ui:widget` names emitted by the backend.
    @MainActor
    static func widgets(
        rootFormData: Binding<FormData>,
        coordinator: NewDiscussionFormCoordinator,
        attachmentsCoordinator: NewDiscussionAttachmentsCoordinator
    ) -> [String: JSONSchemaFormWidget] {
        [
            "glassText": { context in AnyView(GlassTextWidget(context: context)) },
            "glassMenu": { context in AnyView(GlassMenuWidget(context: context, rootFormData: rootFormData)) },
            "glassStepper": { context in AnyView(GlassStepperWidget(context: context)) },
            "glassToggle": { context in AnyView(GlassToggleWidget(context: context)) },
            "discussionPicker": { context in
                AnyView(DiscussionPickerWidget(context: context, rootFormData: rootFormData, coordinator: coordinator))
            },
            "attachmentsPicker": { context in
                AnyView(AttachmentsPickerWidget(context: context, rootFormData: rootFormData, coordinator: attachmentsCoordinator))
            },
        ]
    }

    /// Object templates: a grouped glass card (`card`) for the reference/settings
    /// groups, and a plain vertical stack as the default for the root and prompt.
    static func templates() -> JSONSchemaFormTemplates {
        JSONSchemaFormTemplates(
            objects: ["card": { context in AnyView(GlassCardGroup(context: context)) }],
            defaultObject: { context in AnyView(PlainGroup(context: context)) }
        )
    }
}

// MARK: - Context helpers

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

/// Shared leading icon for the glass rows.
private struct RowIcon: View {
    let systemName: String?
    var body: some View {
        if let systemName {
            Image(systemName: systemName)
                .foregroundStyle(Theme.accent)
                .frame(width: 22)
        }
    }
}

private struct RowChevron: View {
    var body: some View {
        Image(systemName: "chevron.up.chevron.down")
            .font(.footnote.weight(.semibold))
            .foregroundStyle(Theme.secondaryText)
    }
}

// MARK: - Object templates

/// Default plain stack used for the root form and the prompt group: stacks each
/// child with the same 20pt rhythm as the original hand-built form.
private struct PlainGroup: View {
    let context: JSONSchemaFormObjectTemplateContext
    var body: some View {
        VStack(alignment: .leading, spacing: 20) {
            ForEach(context.properties) { property in
                property.content
            }
        }
    }
}

/// Grouped glass card with dividers between rows, matching the original options card.
private struct GlassCardGroup: View {
    let context: JSONSchemaFormObjectTemplateContext
    var body: some View {
        VStack(spacing: 0) {
            ForEach(Array(context.properties.enumerated()), id: \.element.id) { index, property in
                if index > 0 {
                    Divider()
                        .overlay(Theme.divider.opacity(0.5))
                        .padding(.leading, 46)
                }
                property.content
            }
        }
        .glassEffect(in: .rect(cornerRadius: 16))
    }
}

// MARK: - Leaf widgets

/// Text field rendered as a glass text box with a caption: the multi-line
/// topic box by default, or a single-line input when the backend sets
/// `ui:options.multiline` to false.
private struct GlassTextWidget: View {
    let context: JSONSchemaFormWidgetContext
    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(context.fieldTitle).font(.headline)
            textField
                .textFieldStyle(.plain)
                .padding(12)
                .glassEffect(in: .rect(cornerRadius: 16))
                .accessibilityIdentifier(context.accessibilityID ?? "newPlan.field")
            if let description = context.fieldDescription, !description.isEmpty {
                Text(description)
                    .font(.caption)
                    .foregroundStyle(Theme.secondaryText)
            }
        }
    }

    @ViewBuilder
    private var textField: some View {
        if context.isMultiline {
            TextField(context.placeholder ?? "", text: context.stringValue, axis: .vertical)
                .lineLimit(10...15)
        } else {
            TextField(context.placeholder ?? "", text: context.stringValue)
        }
    }
}

/// Enum row rendered as a menu picker inside a glass row (no empty/null option).
private struct GlassMenuWidget: View {
    let context: JSONSchemaFormWidgetContext
    let rootFormData: Binding<FormData>

    private var options: [(value: String, label: String)] {
        if let selectedType, let scoped = context.enumOptions(forType: selectedType) {
            return scoped
        }
        return context.enumOptions
    }

    private var selectedType: String? {
        guard context.propertyName == "template",
              case .object(let root) = rootFormData.wrappedValue,
              case .object(let settings)? = root["settings"],
              case .string(let type)? = settings["type"] else {
            return nil
        }
        return type
    }

    private var currentLabel: String {
        let selected = context.stringValue.wrappedValue
        return options.first(where: { $0.value == selected })?.label ?? selected
    }

    var body: some View {
        Menu {
            Picker(context.fieldTitle, selection: context.stringValue) {
                ForEach(options, id: \.value) { option in
                    Text(option.label).tag(option.value)
                }
            }
        } label: {
            HStack(spacing: 12) {
                RowIcon(systemName: context.icon)
                VStack(alignment: .leading, spacing: 2) {
                    Text(context.fieldTitle)
                        .font(.headline)
                        .foregroundStyle(.primary)
                    Text(currentLabel)
                        .font(.subheadline)
                        .foregroundStyle(Theme.secondaryText)
                }
                Spacer()
                RowChevron()
            }
            .padding(12)
        }
        .tint(Theme.accent)
        .onAppear(perform: normalizeSelection)
        .onChange(of: selectedType) { _, _ in normalizeSelection() }
    }

    private func normalizeSelection() {
        guard let first = options.first else { return }
        if !options.contains(where: { $0.value == context.stringValue.wrappedValue }) {
            context.stringValue.wrappedValue = first.value
        }
    }
}

/// Integer row rendered as a stepper inside a glass row.
private struct GlassStepperWidget: View {
    let context: JSONSchemaFormWidgetContext

    private var range: ClosedRange<Int> {
        let lower = context.schema.integerSchema?.minimum.map { Int($0) } ?? 2
        let upper = context.schema.integerSchema?.maximum.map { Int($0) } ?? 6
        return lower <= upper ? lower...upper : 2...6
    }

    var body: some View {
        HStack(spacing: 12) {
            RowIcon(systemName: context.icon)
            VStack(alignment: .leading, spacing: 2) {
                Text(context.fieldTitle)
                    .font(.headline)
                    .foregroundStyle(.primary)
                Text("\(context.intValue.wrappedValue) people")
                    .font(.subheadline)
                    .foregroundStyle(Theme.secondaryText)
            }
            Spacer()
            Stepper(context.fieldTitle, value: context.intValue, in: range)
                .labelsHidden()
        }
        .padding(12)
    }
}

/// Boolean row rendered as a toggle inside a glass row.
private struct GlassToggleWidget: View {
    let context: JSONSchemaFormWidgetContext
    var body: some View {
        HStack(spacing: 12) {
            RowIcon(systemName: context.icon)
            VStack(alignment: .leading, spacing: 2) {
                Text(context.fieldTitle)
                    .font(.headline)
                    .foregroundStyle(.primary)
                if let description = context.fieldDescription, !description.isEmpty {
                    Text(description)
                        .font(.subheadline)
                        .foregroundStyle(Theme.secondaryText)
                }
            }
            Spacer()
            Toggle(context.fieldTitle, isOn: context.boolValue)
                .labelsHidden()
                .tint(Theme.accent)
        }
        .padding(12)
    }
}

/// Parent-discussion row. Shows the current selection and opens the searchable
/// picker sheet through the form coordinator using the backend-declared deep link.
private struct DiscussionPickerWidget: View {
    let context: JSONSchemaFormWidgetContext
    let rootFormData: Binding<FormData>
    let coordinator: NewDiscussionFormCoordinator
    @Environment(AuthManager.self) private var auth
    @State private var isResolving = false

    private var selectedID: String { context.stringValue.wrappedValue }
    private var selectedReference: PodcastReference? { coordinator.reference(for: context.id) }

    var body: some View {
        HStack(spacing: 12) {
            RowIcon(systemName: context.icon)
            VStack(alignment: .leading, spacing: 2) {
                Text(context.fieldTitle)
                    .font(.headline)
                    .foregroundStyle(.primary)
                Text(subtitle)
                    .font(.subheadline)
                    .foregroundStyle(Theme.secondaryText)
                    .lineLimit(1)
            }
            Spacer()
            if !selectedID.isEmpty {
                Button {
                    writeSelection("")
                    coordinator.clear(fieldID: context.id)
                } label: {
                    Image(systemName: "xmark.circle.fill")
                        .foregroundStyle(Theme.secondaryText)
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Clear parent discussion")
            }
            RowChevron()
        }
        .padding(12)
        .contentShape(.rect)
        .onTapGesture { present() }
        .task(id: selectedID) { await resolveIfNeeded() }
    }

    private var subtitle: String {
        if let selectedReference {
            return selectedReference.displayTitle
        }
        if selectedID.isEmpty {
            return String(localized: "None", comment: "No parent discussion selected")
        }
        return isResolving
            ? String(localized: "Loading...", comment: "Resolving parent discussion title")
            : selectedID
    }

    private func present() {
        guard let deepLink = context.deepLink else { return }
        coordinator.open(deepLink: deepLink, fieldID: context.id) { reference in
            writeSelection(reference?.id ?? "")
        }
    }

    private func writeSelection(_ id: String) {
        var root = rootFormData.wrappedValue.object ?? [:]
        var reference = root["reference"]?.object ?? [:]
        reference[context.propertyName ?? "discussion_id"] = .string(id)
        root["reference"] = .object(properties: reference)
        rootFormData.wrappedValue = .object(properties: root)
    }

    /// When a parent id is present but its title isn't cached (pre-filled or
    /// restored), resolve it so the row shows a human-readable title.
    @MainActor
    private func resolveIfNeeded() async {
        guard !selectedID.isEmpty, selectedReference == nil, !isResolving else { return }
        isResolving = true
        defer { isResolving = false }
        if let reference = try? await APIClient(tokens: auth).parentPodcast(id: selectedID) {
            coordinator.cache(reference, for: context.id)
        }
    }
}

/// Attachments row. Renders the picked-file chips and a source menu (Notion,
/// photos, files); each menu item opens the matching picker through the form
/// coordinator using the backend-declared deep link. The coordinator (owned by
/// `NewDiscussionView`) holds the live upload state and writes ready attachments
/// back into the form value here.
private struct AttachmentsPickerWidget: View {
    let context: JSONSchemaFormWidgetContext
    let rootFormData: Binding<FormData>
    let coordinator: NewDiscussionAttachmentsCoordinator
    @Environment(AuthManager.self) private var auth

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(context.fieldTitle).font(.headline)
            if !coordinator.attachments.isEmpty {
                ScrollView(.horizontal, showsIndicators: false) {
                    HStack(spacing: 8) {
                        ForEach(coordinator.attachments) { att in
                            chip(att)
                        }
                    }
                    .padding(.vertical, 2)
                }
            }
            Menu {
                Button {
                    open(.notion)
                } label: {
                    if coordinator.notionStatusLoaded {
                        Label(coordinator.notionConnected ? "Pick Notion Page" : "Connect to Notion",
                              systemImage: coordinator.notionConnected ? "doc.text.magnifyingglass" : "link.badge.plus")
                    } else {
                        Label("Checking Notion", systemImage: "hourglass")
                    }
                }
                .disabled(!coordinator.notionStatusLoaded)
                Button {
                    open(.photos)
                } label: {
                    Label("Photo Library", systemImage: "photo.on.rectangle")
                }
                Button {
                    open(.files)
                } label: {
                    Label("Files", systemImage: "folder")
                }
            } label: {
                attachCard
            }
            .buttonStyle(.plain)
        }
        .task {
            coordinator.configure(api: APIClient(tokens: auth))
            coordinator.bind(fieldID: context.id) { ready in
                writeReadyAttachments(ready)
            }
            await coordinator.loadNotionStatus()
        }
    }

    private func open(_ source: AttachmentSource) {
        guard let deepLink = context.deepLink else { return }
        coordinator.open(deepLink: deepLink, source: source)
    }

    /// Full-width glass card matching the form's other inputs.
    private var attachCard: some View {
        HStack(spacing: 12) {
            RowIcon(systemName: context.icon)
            VStack(alignment: .leading, spacing: 2) {
                Text("Add attachment")
                    .font(.headline)
                    .foregroundStyle(.primary)
                Text(context.fieldDescription ?? String(localized: "Notion, photos, or files", comment: "Attachment picker subtitle"))
                    .font(.subheadline)
                    .foregroundStyle(Theme.secondaryText)
            }
            Spacer()
            Image(systemName: "plus.circle.fill")
                .font(.title3.weight(.semibold))
                .foregroundStyle(Theme.accent)
        }
        .padding(12)
        .glassEffect(in: .rect(cornerRadius: 16))
    }

    private func chip(_ att: PendingAttachment) -> some View {
        HStack(spacing: 6) {
            switch att.status {
            case .uploading:
                ProgressView().controlSize(.mini).tint(Theme.accent)
            case .ready:
                Image(systemName: "doc.fill").font(.caption2).foregroundStyle(Theme.accent)
            case .failed:
                Image(systemName: "exclamationmark.triangle.fill").font(.caption2).foregroundStyle(.orange)
            }
            Text(att.filename)
                .font(.caption.weight(.medium))
                .lineLimit(1)
                .foregroundStyle(.primary)
            Button {
                coordinator.remove(att.id)
            } label: {
                Image(systemName: "xmark.circle.fill")
                    .font(.caption2)
                    .foregroundStyle(Theme.secondaryText)
            }
            .buttonStyle(.plain)
        }
        .padding(.horizontal, 10)
        .padding(.vertical, 6)
        .glassEffect(in: .capsule)
    }

    /// Encode the ready attachments as the array form value (keys match the Go
    /// `planner.Attachment` json tags via `Attachment`'s CodingKeys).
    static func formData(from attachments: [Attachment]) -> FormData {
        guard let data = try? JSONEncoder().encode(attachments),
              let decoded = try? JSONDecoder().decode(FormData.self, from: data)
        else {
            return .array(items: [])
        }
        return decoded
    }

    private func writeReadyAttachments(_ attachments: [Attachment]) {
        guard let propertyName = context.propertyName else {
            context.formData.wrappedValue = Self.formData(from: attachments)
            return
        }
        var root = rootFormData.wrappedValue.object ?? [:]
        root[propertyName] = Self.formData(from: attachments)
        rootFormData.wrappedValue = .object(properties: root)
    }
}
