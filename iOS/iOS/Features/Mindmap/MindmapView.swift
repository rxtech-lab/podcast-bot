import Photos
import SwiftUI

/// The discussion mindmap sheet: loads the generated tree, renders it in the
/// zoomable/pannable canvas, and — for the owner — layers a selection action
/// bar for adding, renaming, and deleting nodes. Edits autosave (debounced)
/// and are flushed on Done.
struct MindmapView: View {
    let discussionID: String
    var title: String = "Mindmap"
    let isEditable: Bool
    let api: APIClient

    @Environment(\.dismiss) private var dismiss
    @State private var model: MindmapViewModel?
    @State private var editTarget: MindmapEditTarget?
    @State private var editTitle = ""
    @State private var editNote = ""
    @State private var showingDeleteConfirm = false
    @State private var showingExportOptions = false
    @State private var svgExportDocument: MindmapSVGDocument?
    @State private var exportResultMessage: String?

    var body: some View {
        NavigationStack {
            content
                .navigationTitle(title)
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    ToolbarItem(placement: .topBarLeading) {
                        Button("Done") {
                            let model = model
                            Task { @MainActor in
                                await model?.saveNow()
                            }
                            dismiss()
                        }
                    }
                    if isEditable {
                        ToolbarItem(placement: .topBarTrailing) {
                            saveStateIndicator
                        }
                        ToolbarItem(placement: .topBarTrailing) {
                            Button {
                                model?.undo()
                            } label: {
                                Image(systemName: "arrow.uturn.backward")
                            }
                            .disabled(model?.canUndo != true)
                            .accessibilityLabel("Undo")
                        }
                    }
                    ToolbarItem(placement: .bottomBar) {
                        Button {
                            showingExportOptions = true
                        } label: {
                            Label("Export as SVG", systemImage: "square.and.arrow.up")
                        }
                        .disabled(model?.root == nil)
                    }
                }
                .safeAreaInset(edge: .bottom, spacing: 0) {
                    if let model, model.selectedID != nil {
                        selectionBar(model)
                    }
                }
                .alert(editTarget?.isNew == true ? "New idea" : "Edit node", isPresented: editAlertPresented) {
                    TextField("Title", text: $editTitle)
                    TextField("Note (optional)", text: $editNote)
                    Button("Cancel", role: .cancel) { editTarget = nil }
                    Button("Save") { commitEdit() }
                } message: {
                    Text("Give this idea a short title.")
                }
                .confirmationDialog(
                    "Delete this node and everything under it?",
                    isPresented: $showingDeleteConfirm,
                    titleVisibility: .visible
                ) {
                    Button("Delete", role: .destructive) {
                        if let model, let id = model.selectedID {
                            withAnimation(.snappy) { model.delete(id: id) }
                        }
                    }
                    Button("Cancel", role: .cancel) {}
                }
                .confirmationDialog(
                    "Export as SVG",
                    isPresented: $showingExportOptions,
                    titleVisibility: .visible
                ) {
                    Button("Save to Files") { beginFileExport() }
                    Button("Save to Camera Roll") { saveToCameraRoll() }
                    Button("Cancel", role: .cancel) {}
                }
                .fileExporter(
                    isPresented: fileExporterPresented,
                    document: svgExportDocument,
                    contentType: .svg,
                    defaultFilename: exportFilename
                ) { result in
                    if case .failure = result {
                        exportResultMessage = String(localized: "Could not save the SVG file.")
                    }
                    svgExportDocument = nil
                }
                .alert(exportResultMessage ?? "", isPresented: exportAlertPresented) {
                    Button("OK", role: .cancel) { exportResultMessage = nil }
                }
                .task {
                    if model == nil {
                        model = MindmapViewModel(discussionID: discussionID, isEditable: isEditable, api: api)
                    }
                    await model?.load()
                }
        }
    }

    @ViewBuilder
    private var content: some View {
        ZStack {
            Theme.background.ignoresSafeArea()
            if let model {
                switch model.phase {
                case .loading:
                    ProgressView().tint(Theme.accent)
                case .failed(let message):
                    failedState(message)
                case .ready:
                    MindmapCanvasView(model: model)
                }
            } else {
                ProgressView().tint(Theme.accent)
            }
        }
    }

    private func failedState(_ message: String) -> some View {
        VStack(spacing: 12) {
            Image(systemName: "point.3.connected.trianglepath.dotted")
                .font(.largeTitle)
                .foregroundStyle(Theme.secondaryText)
            Text(message)
                .font(.subheadline)
                .foregroundStyle(Theme.secondaryText)
                .multilineTextAlignment(.center)
            Button("Retry") {
                Task { await model?.load() }
            }
            .buttonStyle(.bordered)
            .tint(Theme.accent)
        }
        .padding(32)
    }

    @ViewBuilder
    private var saveStateIndicator: some View {
        switch model?.saveState {
        case .saving:
            ProgressView().controlSize(.small)
        case .saved:
            Image(systemName: "checkmark.circle")
                .foregroundStyle(.green)
                .accessibilityLabel("Saved")
        case .failed:
            Image(systemName: "exclamationmark.triangle")
                .foregroundStyle(.orange)
                .accessibilityLabel("Save failed")
        default:
            EmptyView()
        }
    }

    /// Bottom bar shown while a node is selected: title/note recap for every
    /// viewer, plus the edit actions for the owner.
    private func selectionBar(_ model: MindmapViewModel) -> some View {
        VStack(alignment: .leading, spacing: 10) {
            if let node = model.selectedNode {
                VStack(alignment: .leading, spacing: 2) {
                    Text(node.title)
                        .font(.subheadline.bold())
                        .lineLimit(2)
                    if let note = node.note, !note.isEmpty {
                        Text(note)
                            .font(.caption)
                            .foregroundStyle(Theme.secondaryText)
                            .lineLimit(3)
                    }
                }
            }
            if isEditable {
                HStack(spacing: 0) {
                    selectionAction("Add child", systemImage: "plus") {
                        if let id = model.selectedID, let newID = model.addChild(under: id) {
                            beginEdit(target: MindmapEditTarget(id: newID, isNew: true), model: model)
                        }
                    }
                    selectionAction("Add sibling", systemImage: "plus.square.on.square", disabled: model.selectedIsRoot) {
                        if let id = model.selectedID, let newID = model.addSibling(of: id) {
                            beginEdit(target: MindmapEditTarget(id: newID, isNew: true), model: model)
                        }
                    }
                    selectionAction("Edit", systemImage: "pencil") {
                        if let id = model.selectedID {
                            beginEdit(target: MindmapEditTarget(id: id, isNew: false), model: model)
                        }
                    }
                    selectionAction("Delete", systemImage: "trash", role: .destructive, disabled: model.selectedIsRoot) {
                        showingDeleteConfirm = true
                    }
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(14)
        .glassCard(cornerRadius: 18)
        .padding(.horizontal, 12)
        .padding(.bottom, 8)
    }

    /// An icon-only circular action; the name is exposed to VoiceOver instead
    /// of a visible caption so localized labels can never truncate.
    private func selectionAction(_ title: LocalizedStringKey,
                                 systemImage: String,
                                 role: ButtonRole? = nil,
                                 disabled: Bool = false,
                                 action: @escaping () -> Void) -> some View {
        let tint: Color = role == .destructive ? .red : Theme.accent
        return Button(role: role, action: action) {
            Image(systemName: systemImage)
                .font(.body.weight(.medium))
                .foregroundStyle(tint)
                .frame(width: 46, height: 46)
                .background(tint.opacity(0.12), in: Circle())
        }
        .buttonStyle(.plain)
        .disabled(disabled)
        .opacity(disabled ? 0.35 : 1)
        .accessibilityLabel(Text(title))
        .frame(maxWidth: .infinity)
    }

    // MARK: - SVG export

    /// The exported picture always shows the full tree (fold state ignored),
    /// matching the server-side Notion export.
    private func beginFileExport() {
        guard let root = model?.root else { return }
        svgExportDocument = MindmapSVGDocument(svg: MindmapSVGExporter.svg(root: root))
    }

    /// The photo library cannot store SVGs, so Save to Camera Roll rasterizes
    /// the same picture to a PNG at 3x.
    private func saveToCameraRoll() {
        guard let root = model?.root else { return }
        Task { @MainActor in
            let renderer = ImageRenderer(
                content: MindmapExportRenderView(root: root)
                    .environment(\.colorScheme, .light)
            )
            renderer.scale = 3
            guard let image = renderer.uiImage else {
                exportResultMessage = String(localized: "Could not render the mindmap.")
                return
            }
            let status = await PHPhotoLibrary.requestAuthorization(for: .addOnly)
            guard status == .authorized || status == .limited else {
                exportResultMessage = String(localized: "Photo access was not granted.")
                return
            }
            do {
                try await PHPhotoLibrary.shared().performChanges {
                    PHAssetChangeRequest.creationRequestForAsset(from: image)
                }
                exportResultMessage = String(localized: "Saved to Camera Roll.")
            } catch {
                exportResultMessage = String(localized: "Could not save to Camera Roll.")
            }
        }
    }

    private var exportFilename: String {
        let sanitized = title
            .components(separatedBy: CharacterSet(charactersIn: "/\\:?%*|\"<>"))
            .joined()
            .trimmingCharacters(in: .whitespacesAndNewlines)
        return sanitized.isEmpty ? "Mindmap" : sanitized
    }

    private var fileExporterPresented: Binding<Bool> {
        Binding(
            get: { svgExportDocument != nil },
            set: { if !$0 { svgExportDocument = nil } }
        )
    }

    private var exportAlertPresented: Binding<Bool> {
        Binding(
            get: { exportResultMessage != nil },
            set: { if !$0 { exportResultMessage = nil } }
        )
    }

    private var editAlertPresented: Binding<Bool> {
        Binding(
            get: { editTarget != nil },
            set: { if !$0 { editTarget = nil } }
        )
    }

    private func beginEdit(target: MindmapEditTarget, model: MindmapViewModel) {
        let node = model.root?.node(withID: target.id)
        editTitle = target.isNew ? "" : (node?.title ?? "")
        editNote = node?.note ?? ""
        editTarget = target
    }

    private func commitEdit() {
        guard let model, let target = editTarget else { return }
        let title = editTitle.trimmingCharacters(in: .whitespacesAndNewlines)
        if !title.isEmpty {
            model.rename(id: target.id, title: title, note: editNote)
        }
        editTarget = nil
    }
}

private struct MindmapEditTarget: Identifiable {
    let id: String
    /// A freshly inserted node: the prompt starts empty and is titled "New idea".
    let isNew: Bool
}
