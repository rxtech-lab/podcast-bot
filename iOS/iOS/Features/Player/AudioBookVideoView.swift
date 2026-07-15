import AVKit
import Kingfisher
import MarkdownUI
import Photos
import PhotosUI
import RxAuthSwift
import SwiftUI
import TipKit
import UIKit
import UniformTypeIdentifiers
import os

struct AudioBookVideoView: View {
    let url: URL
    @Environment(\.dismiss) private var dismiss
    @State private var player: AVPlayer?
    @State private var localFile: URL?
    @State private var isDownloading = false
    @State private var message: String?
    @State private var showingShareSheet = false
    @State private var showChrome = false
    @State private var hideChromeTask: Task<Void, Never>?

    private var chromeVisible: Bool {
        showChrome || isDownloading || message != nil
    }

    var body: some View {
        ZStack(alignment: .top) {
            Color.black.ignoresSafeArea()
            VideoPlayer(player: player)
                .ignoresSafeArea()
                .contentShape(Rectangle())
                .simultaneousGesture(TapGesture().onEnded {
                    toggleChromeFromVideoTap()
                })

            VStack(spacing: 0) {
                HStack(spacing: 12) {
                    Button {
                        dismiss()
                    } label: {
                        Image(systemName: "xmark.circle.fill")
                            .font(.system(size: 30))
                            .symbolRenderingMode(.hierarchical)
                            .foregroundStyle(.white)
                    }
                    .accessibilityLabel("Close")

                    Spacer()

                    Button {
                        revealChromeTemporarily()
                        shareVideo()
                    } label: {
                        Image(systemName: "folder")
                            .font(.system(size: 22, weight: .semibold))
                            .foregroundStyle(.white)
                            .frame(width: 38, height: 38)
                    }
                    .disabled(isDownloading)
                    .accessibilityLabel("Save to Files")

                    Button {
                        revealChromeTemporarily()
                        saveToCameraRoll()
                    } label: {
                        Image(systemName: "square.and.arrow.down")
                            .font(.system(size: 22, weight: .semibold))
                            .foregroundStyle(.white)
                            .frame(width: 38, height: 38)
                    }
                    .disabled(isDownloading)
                    .accessibilityLabel("Save to Camera Roll")
                }
                .padding(.horizontal, 16)
                .padding(.top, 58)
                .padding(.bottom, 12)

                if isDownloading || message != nil {
                    HStack(spacing: 10) {
                        if isDownloading {
                            ProgressView().tint(.white)
                        }
                        if let message {
                            Text(message)
                                .font(.caption.weight(.semibold))
                                .foregroundStyle(.white)
                        }
                    }
                    .padding(.horizontal, 12)
                    .padding(.vertical, 8)
                    .background(.black.opacity(0.62), in: .capsule)
                }

                Spacer()
            }
            .opacity(chromeVisible ? 1 : 0)
            .allowsHitTesting(chromeVisible)
            .animation(.easeInOut(duration: 0.18), value: chromeVisible)
        }
        .sheet(isPresented: $showingShareSheet) {
            if let localFile {
                FileShareSheet(url: localFile)
            }
        }
        .onAppear {
            let p = AVPlayer(url: url)
            player = p
            p.play()
        }
        .onDisappear {
            hideChromeTask?.cancel()
            player?.pause()
            player = nil
        }
    }

    private func toggleChromeFromVideoTap() {
        if isDownloading || message != nil {
            revealChromeTemporarily()
            return
        }
        if chromeVisible {
            hideChromeTask?.cancel()
            showChrome = false
        } else {
            revealChromeTemporarily()
        }
    }

    private func revealChromeTemporarily() {
        hideChromeTask?.cancel()
        showChrome = true
        hideChromeTask = Task { @MainActor in
            try? await Task.sleep(for: .seconds(3))
            if !isDownloading && message == nil {
                showChrome = false
            }
        }
    }

    private func shareVideo() {
        revealChromeTemporarily()
        Task {
            if let file = await localVideoFile() {
                localFile = file
                showingShareSheet = true
            }
        }
    }

    private func saveToCameraRoll() {
        revealChromeTemporarily()
        Task {
            guard let file = await localVideoFile() else { return }
            let status = await PHPhotoLibrary.requestAuthorization(for: .addOnly)
            guard status == .authorized || status == .limited else {
                showMessage("Photo access was not granted.")
                return
            }
            do {
                try await PHPhotoLibrary.shared().performChanges {
                    PHAssetChangeRequest.creationRequestForAssetFromVideo(atFileURL: file)
                }
                showMessage("Saved to Camera Roll.")
            } catch {
                showMessage("Could not save video.")
            }
        }
    }

    @MainActor
    private func localVideoFile() async -> URL? {
        if let localFile { return localFile }
        isDownloading = true
        message = "Preparing video..."
        defer { isDownloading = false }
        do {
            let (downloaded, _) = try await URLSession.shared.download(from: url)
            let destination = FileManager.default.temporaryDirectory
                .appendingPathComponent("audiobook-video-\(UUID().uuidString).mp4")
            if FileManager.default.fileExists(atPath: destination.path) {
                try FileManager.default.removeItem(at: destination)
            }
            try FileManager.default.moveItem(at: downloaded, to: destination)
            localFile = destination
            message = nil
            return destination
        } catch {
            showMessage("Could not download video.")
            return nil
        }
    }

    private func showMessage(_ text: String) {
        message = text
        revealChromeTemporarily()
        Task { @MainActor in
            try? await Task.sleep(for: .seconds(2))
            if message == text {
                message = nil
            }
        }
    }
}
