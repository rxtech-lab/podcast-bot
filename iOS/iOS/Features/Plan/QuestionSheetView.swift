import SwiftUI

/// Bottom-sheet question form shown when the planning agent asks the user one or
/// more structured questions. Ported from linda-assistant's QuestionSheetView,
/// adapted to debate-bot's Theme. Supports boolean / single_choice /
/// multiple_choice / fill_in_blank, an "Other" custom answer, and a "Skip All
/// Questions" reject path.
struct QuestionSheetView: View {
    let question: QuestionPayload
    let remainingCount: Int
    let onAnswer: ([[String: AnyCodable]]) -> Void
    let onReject: () -> Void

    @Environment(\.dismiss) private var dismiss
    @State private var answers: [Int: QuestionAnswer] = [:]
    @State private var isLoading = false
    @State private var currentQuestionIndex = 0

    private var totalQuestions: Int { question.questions.count }
    private var isLastQuestion: Bool { currentQuestionIndex >= totalQuestions - 1 }
    private var currentQuestion: QuestionItem? {
        guard currentQuestionIndex < question.questions.count else { return nil }
        return question.questions[currentQuestionIndex]
    }

    private var currentAnswerProvided: Bool { answers[currentQuestionIndex] != nil }
    private var allAnswered: Bool { question.questions.indices.allSatisfy { answers[$0] != nil } }

    var body: some View {
        NavigationStack {
            VStack(spacing: 0) {
                topBar
                if totalQuestions > 1 {
                    questionNavigator
                        .padding(.top, 12)
                        .padding(.bottom, 4)
                }
                ScrollView {
                    VStack(spacing: 0) {
                        if let currentQuestion {
                            questionContentView(index: currentQuestionIndex, item: currentQuestion)
                                .transition(.asymmetric(
                                    insertion: .move(edge: .trailing).combined(with: .opacity),
                                    removal: .move(edge: .leading).combined(with: .opacity)
                                ))
                                .id(currentQuestionIndex)
                        }
                    }
                    .padding(.horizontal, 20)
                    .padding(.top, 16)
                    .padding(.bottom, 120)
                }
                bottomActionBar
            }
            .background(Theme.background.ignoresSafeArea())
            .onTapGesture {
                UIApplication.shared.sendAction(#selector(UIResponder.resignFirstResponder), to: nil, from: nil, for: nil)
            }
            .navigationBarTitleDisplayMode(.inline)
        }
        .presentationDetents([.medium, .large])
        .presentationDragIndicator(.visible)
        .interactiveDismissDisabled(isLoading)
    }
}

// MARK: - Answer state

private enum QuestionAnswer {
    case boolean(Bool)
    case singleChoice(String)
    case multipleChoice(Set<String>)
    case fillInBlank(String)
    case customText(String)
}

// MARK: - Top bar

private extension QuestionSheetView {
    var topBar: some View {
        HStack {
            VStack(alignment: .leading, spacing: 2) {
                Text("Assistant has a question")
                    .font(.headline)
                if remainingCount > 0 {
                    Text("+\(remainingCount) more pending")
                        .font(.caption)
                        .foregroundStyle(Theme.accent)
                }
            }
            Spacer()
            Button { dismiss() } label: {
                Image(systemName: "xmark")
                    .foregroundStyle(Theme.secondaryText)
            }
            .padding()
            .glassEffect(in: Circle())
            .disabled(isLoading)
        }
        .padding(.horizontal, 20)
        .padding(.top, 16)
        .padding(.bottom, 8)
    }
}

// MARK: - Navigator

private extension QuestionSheetView {
    var questionNavigator: some View {
        ScrollViewReader { proxy in
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 8) {
                    ForEach(0 ..< totalQuestions, id: \.self) { index in
                        questionPill(index: index).id(index)
                    }
                }
                .padding(.horizontal, 20)
            }
            .onChange(of: currentQuestionIndex) { _, newValue in
                withAnimation(.easeInOut(duration: 0.25)) { proxy.scrollTo(newValue, anchor: .center) }
            }
        }
    }

    func questionPill(index: Int) -> some View {
        let isActive = index == currentQuestionIndex
        let isAnswered = answers[index] != nil
        let item = question.questions[index]
        return Button {
            withAnimation(.spring(response: 0.35, dampingFraction: 0.8)) { currentQuestionIndex = index }
            triggerHaptic()
        } label: {
            HStack(spacing: 6) {
                if isAnswered {
                    Image(systemName: "checkmark.circle.fill")
                        .font(.system(size: 13, weight: .semibold))
                        .foregroundStyle(.white)
                }
                Text(pillLabel(index: index, item: item))
                    .font(.subheadline.weight(isActive ? .semibold : .medium))
                    .foregroundStyle(isActive || isAnswered ? .white : .primary)
                    .lineLimit(1)
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 8)
            .background { Capsule().fill(pillColor(isActive: isActive, isAnswered: isAnswered)) }
        }
        .buttonStyle(.plain)
        .disabled(isLoading)
    }

    func pillLabel(index: Int, item: QuestionItem) -> String {
        if totalQuestions <= 3 {
            let title = item.title
            return title.count > 20 ? String(title.prefix(18)) + "..." : title
        }
        return "Q\(index + 1)"
    }

    func pillColor(isActive: Bool, isAnswered: Bool) -> Color {
        if isActive { return Theme.accent }
        if isAnswered { return Theme.accent.opacity(0.7) }
        return Theme.accent.opacity(0.3)
    }
}

// MARK: - Content

private extension QuestionSheetView {
    func questionContentView(index: Int, item: QuestionItem) -> some View {
        VStack(alignment: .leading, spacing: 0) {
            questionHeader(item: item).padding(20)
            Divider().padding(.horizontal, 16)
            Group {
                switch item.type {
                case "boolean": booleanInput(index: index)
                case "single_choice": singleChoiceInput(index: index, options: item.options ?? [])
                case "multiple_choice": multipleChoiceInput(index: index, options: item.options ?? [])
                default: fillInBlankInput(index: index)
                }
            }
            .padding(20)
        }
    }

    func questionHeader(item: QuestionItem) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 8) {
                Image(systemName: questionTypeIcon(item.type))
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(Theme.accent)
                    .padding(5)
                    .background { Circle().fill(Theme.accent.opacity(0.12)) }
                Text(questionTypeLabel(item.type))
                    .font(.caption.weight(.medium))
                    .foregroundStyle(.secondary)
                    .textCase(.uppercase)
                Spacer()
                if totalQuestions > 1 {
                    Text("\(currentQuestionIndex + 1)/\(totalQuestions)")
                        .font(.caption.weight(.medium))
                        .foregroundStyle(.tertiary)
                        .monospacedDigit()
                }
            }
            Text(item.title)
                .font(.title3.weight(.semibold))
                .foregroundStyle(.primary)
                .fixedSize(horizontal: false, vertical: true)
                .padding(.top, 4)
            if let description = item.description, !description.isEmpty {
                Text(description)
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
    }

    func questionTypeIcon(_ type: String) -> String {
        switch type {
        case "boolean": "hand.point.up.left.fill"
        case "single_choice": "list.bullet"
        case "multiple_choice": "checklist"
        default: "text.cursor"
        }
    }

    func questionTypeLabel(_ type: String) -> String {
        switch type {
        case "boolean": "Yes or No"
        case "single_choice": "Choose one"
        case "multiple_choice": "Select all"
        default: "Free text"
        }
    }

    func booleanInput(index: Int) -> some View {
        let current: Bool? = { if case let .boolean(v) = answers[index] { return v }; return nil }()
        return HStack(spacing: 12) {
            BooleanOptionButton(title: "Yes", systemImage: "hand.thumbsup.fill", isSelected: current == true, color: .green) {
                withAnimation(.spring(response: 0.3, dampingFraction: 0.7)) { answers[index] = .boolean(true) }
                triggerHaptic()
            }
            BooleanOptionButton(title: "No", systemImage: "hand.thumbsdown.fill", isSelected: current == false, color: .red) {
                withAnimation(.spring(response: 0.3, dampingFraction: 0.7)) { answers[index] = .boolean(false) }
                triggerHaptic()
            }
        }
        .disabled(isLoading)
    }

    func singleChoiceInput(index: Int, options: [QuestionOptionItem]) -> some View {
        let selected: String = {
            if case let .singleChoice(v) = answers[index] { return v }
            if case let .customText(v) = answers[index] { return "__custom__\(v)" }
            return ""
        }()
        let isCustom = selected.hasPrefix("__custom__")
        return VStack(spacing: 8) {
            ForEach(options, id: \.title) { option in
                ChoiceOptionRow(title: option.title, description: option.description, isSelected: selected == option.title, style: .radio) {
                    withAnimation(.spring(response: 0.3, dampingFraction: 0.7)) { answers[index] = .singleChoice(option.title) }
                    triggerHaptic()
                }
            }
            ChoiceOptionRow(title: "Other", description: nil, isSelected: isCustom, style: .radio) {
                withAnimation(.spring(response: 0.3, dampingFraction: 0.7)) { answers[index] = .customText("") }
                triggerHaptic()
            }
            if isCustom {
                CustomTextInput(text: Binding(
                    get: { if case let .customText(v) = answers[index] { return v }; return "" },
                    set: { answers[index] = .customText($0) }
                ), placeholder: "Type your answer...")
                .transition(.opacity.combined(with: .move(edge: .top)))
            }
        }
        .disabled(isLoading)
    }

    func multipleChoiceInput(index: Int, options: [QuestionOptionItem]) -> some View {
        let selected: Set<String> = { if case let .multipleChoice(v) = answers[index] { return v }; return [] }()
        let customText: String = { if case let .customText(v) = answers[index] { return v }; return "" }()
        let hasCustom: Bool = { if case .customText = answers[index] { return true }; return false }()
        return VStack(spacing: 8) {
            ForEach(options, id: \.title) { option in
                ChoiceOptionRow(title: option.title, description: option.description, isSelected: selected.contains(option.title), style: .checkbox) {
                    withAnimation(.spring(response: 0.3, dampingFraction: 0.7)) {
                        var current = selected
                        if current.contains(option.title) { current.remove(option.title) } else { current.insert(option.title) }
                        answers[index] = .multipleChoice(current)
                    }
                    triggerHaptic()
                }
            }
            ChoiceOptionRow(title: "Other", description: nil, isSelected: hasCustom, style: .checkbox) {
                withAnimation(.spring(response: 0.3, dampingFraction: 0.7)) {
                    if hasCustom { answers[index] = .multipleChoice(selected) } else { answers[index] = .customText("") }
                }
                triggerHaptic()
            }
            if hasCustom {
                CustomTextInput(text: Binding(get: { customText }, set: { answers[index] = .customText($0) }), placeholder: "Type your answer...")
                    .transition(.opacity.combined(with: .move(edge: .top)))
            }
        }
        .disabled(isLoading)
    }

    func fillInBlankInput(index: Int) -> some View {
        let text: String = { if case let .fillInBlank(v) = answers[index] { return v }; return "" }()
        return CustomTextInput(text: Binding(
            get: { text },
            set: { answers[index] = .fillInBlank($0) }
        ), placeholder: "Type your answer...", isMultiline: true)
        .disabled(isLoading)
    }
}

// MARK: - Bottom bar

private extension QuestionSheetView {
    var bottomActionBar: some View {
        VStack(spacing: 10) {
            HStack(spacing: 10) {
                if currentQuestionIndex > 0 {
                    Button {
                        withAnimation(.spring(response: 0.35, dampingFraction: 0.8)) { currentQuestionIndex -= 1 }
                        triggerHaptic()
                    } label: {
                        Image(systemName: "chevron.left")
                            .font(.body.weight(.semibold))
                            .frame(width: 44, height: 44)
                    }
                    .buttonStyle(.glass)
                    .tint(.secondary)
                    .disabled(isLoading)
                }
                Button {
                    if isLastQuestion || allAnswered {
                        triggerHaptic()
                        submitAnswers()
                    } else {
                        withAnimation(.spring(response: 0.35, dampingFraction: 0.8)) { currentQuestionIndex += 1 }
                        triggerHaptic()
                    }
                } label: {
                    HStack(spacing: 8) {
                        if isLoading { ProgressView().tint(.white) }
                        Text(mainButtonLabel).font(.body.weight(.semibold))
                        if !isLoading { Image(systemName: mainButtonIcon).font(.body.weight(.semibold)) }
                    }
                    .frame(maxWidth: .infinity, minHeight: 44)
                }
                .buttonStyle(.glassProminent)
                .tint(Theme.accent)
                .disabled(isLoading || !currentAnswerProvided)
            }
            Button(role: .destructive) {
                isLoading = true
                onReject()
            } label: {
                Text("Skip All Questions")
                    .font(.subheadline.weight(.medium))
                    .frame(maxWidth: .infinity, minHeight: 36)
            }
            .buttonStyle(.plain)
            .foregroundStyle(.secondary)
            .disabled(isLoading)
        }
        .padding(.horizontal, 20)
        .padding(.top, 12)
        .padding(.bottom, 16)
        .background { Rectangle().fill(.ultraThinMaterial).ignoresSafeArea() }
    }

    var mainButtonLabel: String { (allAnswered || isLastQuestion) ? "Submit" : "Next" }
    var mainButtonIcon: String { (allAnswered || isLastQuestion) ? "paperplane.fill" : "arrow.right" }

    func submitAnswers() {
        isLoading = true
        var result: [[String: AnyCodable]] = []
        for (index, _) in question.questions.enumerated() {
            guard let answer = answers[index] else { continue }
            var entry: [String: AnyCodable] = ["questionIndex": .int(index)]
            switch answer {
            case let .boolean(v): entry["answer"] = .bool(v)
            case let .singleChoice(v): entry["answer"] = .string(v)
            case let .multipleChoice(v): entry["answer"] = .array(v.sorted().map { .string($0) })
            case let .fillInBlank(v): entry["answer"] = .string(v)
            case let .customText(v): entry["answer"] = .string(v)
            }
            result.append(entry)
        }
        onAnswer(result)
    }
}

private func triggerHaptic() {
    #if !targetEnvironment(simulator)
    UIImpactFeedbackGenerator(style: .light).impactOccurred()
    #endif
}

// MARK: - Reusable components

private struct BooleanOptionButton: View {
    let title: String
    let systemImage: String
    let isSelected: Bool
    let color: Color
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            VStack(spacing: 10) {
                ZStack {
                    Circle()
                        .fill(isSelected ? color.opacity(0.15) : Color(.tertiarySystemGroupedBackground))
                        .frame(width: 52, height: 52)
                    Image(systemName: systemImage)
                        .font(.title3.weight(.semibold))
                        .foregroundStyle(isSelected ? color : .secondary)
                        .contentTransition(.symbolEffect(.replace))
                }
                Text(title)
                    .font(.subheadline.weight(.medium))
                    .foregroundStyle(isSelected ? .primary : .secondary)
            }
            .frame(maxWidth: .infinity)
            .padding(.vertical, 16)
            .background {
                RoundedRectangle(cornerRadius: 14, style: .continuous)
                    .fill(isSelected ? color.opacity(0.06) : Color(.quaternarySystemFill))
            }
            .overlay {
                RoundedRectangle(cornerRadius: 14, style: .continuous)
                    .strokeBorder(isSelected ? color.opacity(0.4) : Color.clear, lineWidth: 1.5)
            }
            .scaleEffect(isSelected ? 1.02 : 1.0)
        }
        .buttonStyle(.plain)
        .animation(.spring(response: 0.3, dampingFraction: 0.7), value: isSelected)
    }
}

private struct ChoiceOptionRow: View {
    enum Style {
        case radio, checkbox
        var selectedIcon: String { self == .radio ? "circle.inset.filled" : "checkmark.square.fill" }
        var deselectedIcon: String { self == .radio ? "circle" : "square" }
    }

    let title: String
    let description: String?
    let isSelected: Bool
    let style: Style
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            HStack(spacing: 12) {
                Image(systemName: isSelected ? style.selectedIcon : style.deselectedIcon)
                    .font(.title3)
                    .foregroundStyle(isSelected ? Theme.accent : Color.secondary)
                    .frame(width: 24)
                    .contentTransition(.symbolEffect(.replace))
                VStack(alignment: .leading, spacing: 2) {
                    Text(title).font(.body).foregroundStyle(.primary)
                    if let description, !description.isEmpty {
                        Text(description).font(.caption).foregroundStyle(.secondary)
                    }
                }
                Spacer()
                if isSelected {
                    Image(systemName: "checkmark")
                        .font(.caption.weight(.bold))
                        .foregroundStyle(Theme.accent)
                        .transition(.scale.combined(with: .opacity))
                }
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 12)
            .background {
                RoundedRectangle(cornerRadius: 10, style: .continuous)
                    .fill(isSelected ? Theme.accent.opacity(0.06) : Color(.quaternarySystemFill))
            }
            .overlay {
                RoundedRectangle(cornerRadius: 10, style: .continuous)
                    .strokeBorder(isSelected ? Theme.accent.opacity(0.25) : Color.clear, lineWidth: 1)
            }
        }
        .buttonStyle(.plain)
        .animation(.spring(response: 0.3, dampingFraction: 0.7), value: isSelected)
    }
}

private struct CustomTextInput: View {
    @Binding var text: String
    let placeholder: String
    var isMultiline: Bool = false
    @FocusState private var isFocused: Bool

    var body: some View {
        Group {
            if isMultiline {
                TextField(placeholder, text: $text, axis: .vertical).lineLimit(6 ... 12).focused($isFocused)
            } else {
                TextField(placeholder, text: $text).focused($isFocused)
            }
        }
        .padding(14)
        .background { RoundedRectangle(cornerRadius: 10, style: .continuous).fill(Color(.quaternarySystemFill)) }
        .overlay {
            RoundedRectangle(cornerRadius: 10, style: .continuous)
                .strokeBorder(isFocused ? Theme.accent.opacity(0.5) : Color.clear, lineWidth: 1.5)
        }
        .animation(.easeInOut(duration: 0.2), value: isFocused)
    }
}
