import SwiftUI
import RevenueCat
import RevenueCatUI

/// App-owned subscription page backed by RevenueCat's Offering and purchase
/// APIs. RevenueCat still supplies the App Store products, localized prices,
/// receipt validation, and entitlements; the visual presentation lives here.
struct PaywallScreen: View {
    @Environment(\.dismiss) private var dismiss
    @Environment(PurchaseManager.self) private var purchases
    @State private var selectedPackageID: String?
    @State private var isPurchasing = false
    @State private var isRestoring = false
    @State private var errorMessage: String?
    @State private var statusMessage: String?
    @State private var hasRequestedOfferings = false

    /// Called when the paywall finishes (close or purchase completes). Defaults
    /// to dismissing this view; the launch flow passes a callback to advance the
    /// coordinator instead.
    var onFinish: (() -> Void)?

    private func finish() {
        if let onFinish { onFinish() } else { dismiss() }
    }

    private var packages: [Package] {
        let available = purchases.offerings?.current?.availablePackages ?? []
        return available.sorted { packageRank($0) < packageRank($1) }
    }

    private var selectedPackage: Package? {
        if let selectedPackageID,
           let selected = packages.first(where: { $0.identifier == selectedPackageID }) {
            return selected
        }
        return packages.first(where: {
            normalizedIdentifier($0).contains("pro")
        }) ?? packages.first
    }

    private var isBusy: Bool {
        isPurchasing || isRestoring
    }

    var body: some View {
        NavigationStack {
            ZStack {
                Theme.background.ignoresSafeArea()

                ScrollView {
                    VStack(spacing: 24) {
                        header
                        benefits

                        if purchases.isPro {
                            subscribedState
                        } else if packages.isEmpty {
                            unavailableState
                        } else {
                            plans
                            purchaseButton
                        }

                        restoreSection
                        renewalDisclosure
                    }
                    .frame(maxWidth: 620)
                    .padding(.horizontal, 20)
                    .padding(.top, 20)
                    .padding(.bottom, 32)
                    .frame(maxWidth: .infinity)
                }

                if isBusy {
                    busyOverlay
                }
            }
            .navigationTitle("Subscription")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                #if os(macOS)
                ToolbarItem(placement: .cancellationAction) {
                    Button("Close", action: finish)
                        .keyboardShortcut(.cancelAction)
                        .disabled(isBusy)
                }
                #else
                ToolbarItem(placement: .topBarLeading) {
                    Button(action: finish) {
                        Image(systemName: "xmark")
                    }
                    .accessibilityLabel("Close")
                    .disabled(isBusy)
                }
                #endif
            }
            .alert("Subscription", isPresented: Binding(
                get: { errorMessage != nil },
                set: { if !$0 { errorMessage = nil } }
            )) {
                Button("OK", role: .cancel) { errorMessage = nil }
            } message: {
                Text(errorMessage ?? "")
            }
            .task {
                if purchases.offerings == nil {
                    await purchases.refreshOfferings()
                }
                hasRequestedOfferings = true
                selectInitialPackage()
            }
            .onChange(of: packages.map(\.identifier)) { _, _ in
                selectInitialPackage()
            }
        }
    }

    private var header: some View {
        VStack(spacing: 14) {
            ZStack {
                Circle()
                    .fill(Theme.accent.opacity(0.14))
                Image(systemName: "waveform.badge.plus")
                    .font(.system(size: 38, weight: .semibold))
                    .foregroundStyle(Theme.accent)
            }
            .frame(width: 82, height: 82)

            VStack(spacing: 8) {
                Text("Create more with \(AppStringLiteral.appTitleRaw)")
                    .font(.largeTitle.bold())
                    .multilineTextAlignment(.center)
                Text("Choose the plan that fits how you create. Prices and billing periods come directly from the App Store.")
                    .font(.body)
                    .foregroundStyle(Theme.secondaryText)
                    .multilineTextAlignment(.center)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
    }

    private var benefits: some View {
        VStack(alignment: .leading, spacing: 13) {
            benefit("Create more podcasts and audiobooks", icon: "waveform")
            benefit("Use subscriber-only models, voices, and tools", icon: "sparkles")
            benefit("Keep purchases and access synced across devices", icon: "arrow.triangle.2.circlepath")
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .glassCard(cornerRadius: 20)
    }

    private func benefit(_ title: LocalizedStringKey, icon: String) -> some View {
        HStack(spacing: 12) {
            Image(systemName: icon)
                .font(.body.weight(.semibold))
                .foregroundStyle(Theme.accent)
                .frame(width: 24)
            Text(title)
                .font(.subheadline.weight(.medium))
            Spacer(minLength: 0)
        }
    }

    private var plans: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Choose a plan")
                .font(.headline)
                .frame(maxWidth: .infinity, alignment: .leading)

            ForEach(packages) { package in
                planCard(package)
            }
        }
    }

    private func planCard(_ package: Package) -> some View {
        let isSelected = selectedPackage?.identifier == package.identifier
        let isRecommended = normalizedIdentifier(package).contains("pro")

        return Button {
            selectedPackageID = package.identifier
            statusMessage = nil
        } label: {
            VStack(spacing: 0) {
                if isRecommended {
                    HStack(spacing: 6) {
                        Image(systemName: "sparkles")
                        Text("MOST ACCESS")
                    }
                    .font(.caption.weight(.bold))
                    .foregroundStyle(.white)
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 7)
                    .background(Theme.accent)
                }

                HStack(alignment: .top, spacing: 14) {
                    Image(systemName: isSelected ? "checkmark.circle.fill" : "circle")
                        .font(.title3)
                        .foregroundStyle(isSelected ? Theme.accent : Theme.secondaryText)

                    VStack(alignment: .leading, spacing: 6) {
                        Text(planTitle(package))
                            .font(.headline)
                            .foregroundStyle(.primary)

                        Text(planDescription(package))
                            .font(.subheadline)
                            .foregroundStyle(Theme.secondaryText)
                            .multilineTextAlignment(.leading)
                            .fixedSize(horizontal: false, vertical: true)
                    }

                    Spacer(minLength: 8)

                    VStack(alignment: .trailing, spacing: 3) {
                        Text(package.storeProduct.localizedPriceString)
                            .font(.headline)
                            .foregroundStyle(.primary)
                        Text(periodLabel(package))
                            .font(.caption)
                            .foregroundStyle(Theme.secondaryText)
                    }
                }
                .padding(16)
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            .background(
                isSelected ? Theme.accent.opacity(0.10) : Theme.rowBackground,
                in: .rect(cornerRadius: 18)
            )
            .clipShape(.rect(cornerRadius: 18))
            .overlay {
                RoundedRectangle(cornerRadius: 18)
                    .stroke(isSelected ? Theme.accent : Theme.divider.opacity(0.5),
                            lineWidth: isSelected ? 2 : 1)
            }
        }
        .buttonStyle(.plain)
        .disabled(isBusy)
        .accessibilityValue(isSelected ? "Selected" : "")
    }

    private var purchaseButton: some View {
        VStack(spacing: 10) {
            Button {
                Task { await purchaseSelectedPackage() }
            } label: {
                Text(selectedPackage.map { "Continue with \(planTitle($0))" } ?? "Continue")
                    .font(.headline)
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 15)
            }
            .buttonStyle(.borderedProminent)
            .buttonBorderShape(.roundedRectangle(radius: 16))
            .tint(Theme.accent)
            .disabled(selectedPackage == nil || isBusy)

            if let statusMessage {
                Text(statusMessage)
                    .font(.footnote)
                    .foregroundStyle(Theme.secondaryText)
                    .multilineTextAlignment(.center)
            }
        }
    }

    private var subscribedState: some View {
        VStack(spacing: 14) {
            Image(systemName: "checkmark.seal.fill")
                .font(.system(size: 42))
                .foregroundStyle(.green)
            Text("Your subscription is active")
                .font(.title3.bold())
            Text("Your subscriber features are ready to use.")
                .font(.subheadline)
                .foregroundStyle(Theme.secondaryText)
            Button("Done", action: finish)
                .buttonStyle(.borderedProminent)
                .tint(Theme.accent)
        }
        .frame(maxWidth: .infinity)
        .glassCard(cornerRadius: 20, tint: .green.opacity(0.12))
    }

    private var unavailableState: some View {
        VStack(spacing: 12) {
            if purchases.isRefreshingOfferings || !hasRequestedOfferings {
                ProgressView()
                    .tint(Theme.accent)
                Text("Loading plans…")
                    .font(.subheadline)
                    .foregroundStyle(Theme.secondaryText)
            } else {
                Image(systemName: "cart.badge.questionmark")
                    .font(.system(size: 34))
                    .foregroundStyle(Theme.secondaryText)
                Text("Plans are temporarily unavailable")
                    .font(.headline)
                Text("Check your App Store connection and try again.")
                    .font(.subheadline)
                    .foregroundStyle(Theme.secondaryText)
                    .multilineTextAlignment(.center)
                Button("Try Again") {
                    Task {
                        await purchases.refreshOfferings()
                        selectInitialPackage()
                    }
                }
                .buttonStyle(.borderedProminent)
                .tint(Theme.accent)
            }
        }
        .frame(maxWidth: .infinity)
        .glassCard(cornerRadius: 20)
    }

    private var restoreSection: some View {
        Button("Restore Purchases") {
            Task { await restorePurchases() }
        }
        .font(.subheadline.weight(.semibold))
        .disabled(isBusy || !purchases.isConfigured)
    }

    private var renewalDisclosure: some View {
        Text("Payment will be charged to your Apple Account. The subscription renews automatically unless canceled at least 24 hours before the end of the current billing period. You can manage or cancel it in App Store account settings.")
            .font(.caption)
            .foregroundStyle(Theme.secondaryText)
            .multilineTextAlignment(.center)
            .fixedSize(horizontal: false, vertical: true)
    }

    private var busyOverlay: some View {
        ZStack {
            Color.black.opacity(0.18).ignoresSafeArea()
            VStack(spacing: 12) {
                ProgressView()
                    .controlSize(.large)
                    .tint(Theme.accent)
                Text(isRestoring ? "Restoring purchases…" : "Completing purchase…")
                    .font(.subheadline.weight(.semibold))
            }
            .padding(24)
            .background(.regularMaterial, in: .rect(cornerRadius: 18))
        }
    }

    private func selectInitialPackage() {
        guard selectedPackageID == nil || !packages.contains(where: { $0.identifier == selectedPackageID }) else {
            return
        }
        selectedPackageID = packages.first(where: {
            normalizedIdentifier($0).contains("pro")
        })?.identifier ?? packages.first?.identifier
    }

    private func purchaseSelectedPackage() async {
        guard let package = selectedPackage else { return }
        errorMessage = nil
        statusMessage = nil
        isPurchasing = true
        defer { isPurchasing = false }

        do {
            if try await purchases.purchase(package: package) {
                finish()
            }
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    private func restorePurchases() async {
        errorMessage = nil
        statusMessage = nil
        isRestoring = true
        defer { isRestoring = false }

        do {
            if try await purchases.restorePurchases() {
                finish()
            } else {
                statusMessage = "No active subscription was found for this Apple Account."
            }
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    private func packageRank(_ package: Package) -> Int {
        let identifier = normalizedIdentifier(package)
        if identifier.contains("plus") { return 0 }
        if identifier.contains("pro") { return 1 }
        return 2
    }

    private func normalizedIdentifier(_ package: Package) -> String {
        "\(package.identifier) \(package.storeProduct.productIdentifier)"
            .lowercased()
    }

    private func planTitle(_ package: Package) -> String {
        let localizedTitle = package.storeProduct.localizedTitle
            .trimmingCharacters(in: .whitespacesAndNewlines)
        if !localizedTitle.isEmpty { return localizedTitle }

        let identifier = normalizedIdentifier(package)
        if identifier.contains("plus") { return "Plus" }
        if identifier.contains("pro") { return "Pro" }
        return "Subscription"
    }

    private func planDescription(_ package: Package) -> String {
        let localizedDescription = package.storeProduct.localizedDescription
            .trimmingCharacters(in: .whitespacesAndNewlines)
        if !localizedDescription.isEmpty { return localizedDescription }

        return "Subscriber access billed through your Apple Account."
    }

    private func periodLabel(_ package: Package) -> String {
        guard let period = package.storeProduct.subscriptionPeriod else {
            return "one-time"
        }

        let unit: String
        switch period.unit {
        case .day: unit = period.value == 1 ? "day" : "days"
        case .week: unit = period.value == 1 ? "week" : "weeks"
        case .month: unit = period.value == 1 ? "month" : "months"
        case .year: unit = period.value == 1 ? "year" : "years"
        @unknown default: unit = "period"
        }
        return period.value == 1 ? "per \(unit)" : "every \(period.value) \(unit)"
    }
}

/// The RevenueCat Customer Center for managing / restoring subscriptions.
struct CustomerCenterScreen: View {
    var showsCloseButton = true

    var body: some View {
        #if os(macOS)
        // RevenueCat's Customer Center isn't available on macOS; point the
        // user at the App Store subscription management page instead.
        VStack(spacing: 16) {
            Image(systemName: "person.crop.circle")
                .font(.largeTitle)
                .foregroundStyle(.secondary)
            Text("Manage your subscription in the App Store.")
                .multilineTextAlignment(.center)
            Link("Manage Subscriptions",
                 destination: URL(string: "https://apps.apple.com/account/subscriptions")!)
        }
        .padding(32)
        .frame(minWidth: 360, minHeight: 240)
        #else
        CustomerCenterView(
            navigationOptions: CustomerCenterNavigationOptions(
                usesExistingNavigation: !showsCloseButton,
                shouldShowCloseButton: showsCloseButton
            )
        )
        #endif
    }
}
