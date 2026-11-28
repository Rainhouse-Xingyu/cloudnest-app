import SwiftUI
import AppKit
import ImageIO
import Photos
import PhotosUI
import QuickLook
import AVFoundation

struct MediaPreviewRequest: Identifiable {
    let id = UUID()
    let selection: MediaPreviewSelection
}

enum MediaImageDecoder {
    static func thumbnail(_ source: CGImageSource, maximumPixel: Int) throws -> NSImage {
        guard let properties = CGImageSourceCopyPropertiesAtIndex(source,0,nil) as? [CFString:Any],
              let width = properties[kCGImagePropertyPixelWidth] as? NSNumber,
              let height = properties[kCGImagePropertyPixelHeight] as? NSNumber,
              width.doubleValue.isFinite, height.doubleValue.isFinite,
              width.doubleValue > 0, height.doubleValue > 0,
              width.doubleValue <= 65535, height.doubleValue <= 65535,
              width.doubleValue * height.doubleValue <= 64_000_000 else { throw ServerError.capacityExceeded }
        let options: [CFString:Any] = [kCGImageSourceCreateThumbnailFromImageAlways:true,
            kCGImageSourceThumbnailMaxPixelSize:maximumPixel,kCGImageSourceCreateThumbnailWithTransform:true,
            kCGImageSourceShouldCacheImmediately:true]
        guard let image = CGImageSourceCreateThumbnailAtIndex(source,0,options as CFDictionary) else { throw ServerError.invalidResponse }
        return NSImage(cgImage:image,size:NSSize(width:image.width,height:image.height))
    }
    static func thumbnail(data: Data) throws -> NSImage {
        guard let source = CGImageSourceCreateWithData(data as CFData,nil),
              let properties = CGImageSourceCopyPropertiesAtIndex(source,0,nil) as? [CFString:Any],
              let width = properties[kCGImagePropertyPixelWidth] as? NSNumber,
              let height = properties[kCGImagePropertyPixelHeight] as? NSNumber,
              (1...256).contains(width.intValue), (1...256).contains(height.intValue) else { throw ServerError.invalidResponse }
        return try thumbnail(source,maximumPixel:256)
    }
    static func thumbnail(url: URL) throws -> NSImage {
        guard let source = CGImageSourceCreateWithURL(url as CFURL,nil) else { throw ServerError.invalidResponse }
        return try thumbnail(source,maximumPixel:1600)
    }
}

@MainActor @Observable
private final class NativeLivePhotoLoader {
    private var request = PHLivePhotoRequestIDInvalid
    private var generation = UUID()
    private var timeout: Task<Void,Never>?
    private var asset: AVURLAsset?
    private(set) var photo: PHLivePhoto?
    private(set) var error: String?
    func load(_ urls: [URL], placeholder: NSImage) async {
        guard !Task.isCancelled else { return }
        clear()
        let id = UUID(); generation = id
        guard urls.count == 2 else { error = "预览资源不完整。"; return }
        let asset = AVURLAsset(url:urls[1]); self.asset = asset
        do {
            let duration = try await asset.load(.duration).seconds
            guard let track = try await asset.loadTracks(withMediaType:.video).first else { throw ServerError.invalidResponse }
            let size = try await track.load(.naturalSize)
            guard duration.isFinite, duration > 0, duration <= 30, size.width.isFinite, size.height.isFinite,
                  size.width > 0, size.height > 0, size.width <= 4096, size.height <= 4096 else { throw ServerError.capacityExceeded }
        } catch {
            if generation == id { self.error = "动态预览支持有效的最长 30 秒、宽高均不超过 4096 像素的片段。可以单独使用 Quick Look。" }
            return
        }
        guard generation == id, !Task.isCancelled else { return }
        timeout = Task {
            do { try await Task.sleep(for:.seconds(15)) } catch { return }
            guard generation == id, photo == nil, error == nil else { return }
            error = "系统未能在时限内完成 Live Photo 验证，可以单独使用 Quick Look。"
            PHLivePhoto.cancelRequest(withRequestID:request); request = PHLivePhotoRequestIDInvalid
        }
        request = PHLivePhoto.request(withResourceFileURLs:urls,placeholderImage:placeholder,
            targetSize:CGSize(width:1280,height:1280),contentMode:.aspectFit) { [weak self] photo,info in
            let failed = info[PHLivePhotoInfoErrorKey] != nil
            let cancelled = info[PHLivePhotoInfoCancelledKey] as? Bool ?? false
            let degraded = info[PHLivePhotoInfoIsDegradedKey] as? Bool ?? false
            Task { @MainActor in
                guard let self, self.generation == id, !cancelled else { return }
                if let photo, !degraded, photo.size.width > 0, photo.size.height > 0, !failed {
                    let resources = PHAssetResource.assetResources(for:photo)
                    guard resources.count == 2, resources.contains(where:{$0.type == .photo}), resources.contains(where:{$0.type == .pairedVideo}) else {
                        self.error = "系统返回的 Live Photo 资源不完整。可以分别使用 Quick Look。"; self.timeout?.cancel(); return
                    }
                    self.photo = photo; self.error = nil; self.timeout?.cancel()
                }
                else if failed { self.error = "系统无法将这两个资源解码为 Live Photo。可以分别预览原图和动态片段；文件关联仍保留。"; self.timeout?.cancel() }
            }
        }
    }
    func clear() {
        generation = UUID()
        timeout?.cancel(); timeout = nil; asset?.cancelLoading(); asset = nil
        if request != PHLivePhotoRequestIDInvalid { PHLivePhoto.cancelRequest(withRequestID:request) }
        request = PHLivePhotoRequestIDInvalid; photo = nil; error = nil
    }
}

private struct NativeLivePhotoView: NSViewRepresentable {
    let photo: PHLivePhoto
    let playback: Int
    let onPlayback: @MainActor (String) -> Void
    @MainActor final class Coordinator: NSObject, PHLivePhotoViewDelegate {
        var playback = 0
        var started: ContinuousClock.Instant?
        var onPlayback: (@MainActor (String) -> Void)?
        func livePhotoView(_ livePhotoView: PHLivePhotoView, willBeginPlaybackWith playbackStyle: PHLivePhotoViewPlaybackStyle) {
            guard playbackStyle == .full else { return }
            started = .now
            Task { @MainActor [weak self] in self?.onPlayback?("正在播放动态效果（静音）") }
        }
        func livePhotoView(_ livePhotoView: PHLivePhotoView, didEndPlaybackWith playbackStyle: PHLivePhotoViewPlaybackStyle) {
            guard playbackStyle == .full else { return }
            let completed = started.map { $0.duration(to:.now) >= .milliseconds(500) } ?? false
            started = nil
            Task { @MainActor [weak self] in self?.onPlayback?(completed ? "动态效果已播放" : "动态播放未能完成，可以使用动态片段 Quick Look。") }
        }
    }
    func makeCoordinator() -> Coordinator { let coordinator = Coordinator(); coordinator.onPlayback = onPlayback; return coordinator }
    func makeNSView(context: Context) -> PHLivePhotoView {
        let view = PHLivePhotoView(); view.contentMode = .aspectFit; view.isMuted = true; view.delegate = context.coordinator
        return view
    }
    func updateNSView(_ view: PHLivePhotoView, context: Context) {
        context.coordinator.onPlayback = onPlayback
        if view.livePhoto !== photo { view.livePhoto = photo }
        if playback != context.coordinator.playback {
            context.coordinator.playback = playback; view.startPlayback(with:.full)
        }
    }
    static func dismantleNSView(_ view: PHLivePhotoView, coordinator: Coordinator) { coordinator.onPlayback = nil; view.delegate = nil; view.stopPlayback(); view.livePhoto = nil }
}

struct MediaPreviewSheet: View {
    let request: MediaPreviewRequest
    @Environment(\.dismiss) private var dismiss
    @State private var state: MediaPreviewState
    @State private var live = NativeLivePhotoLoader()
    @State private var image: NSImage?
    @State private var imageError: String?
    @State private var quickLook: URL?
    @State private var playback = 0
    @State private var closing = false
    @State private var liveTask: Task<Void,Never>?
    @State private var playbackStatus: String?

    init(request: MediaPreviewRequest, session: SessionState, transfers: TransferManager) {
        self.request = request; _state = State(initialValue:MediaPreviewState(session:session,transfers:transfers))
    }
    private var isLive: Bool { if case .livePhoto = request.selection { true } else { false } }
    var body: some View {
        VStack(alignment:.leading,spacing:12) {
            Text(isLive ? "Live Photo 预览" : "照片预览").font(.title2)
            Text("下载并校验所选版本的临时副本，关闭后清理。不写入系统照片图库。")
                .font(.callout).foregroundStyle(.secondary).fixedSize(horizontal:false,vertical:true)
            if let photo = live.photo {
                NativeLivePhotoView(photo:photo,playback:playback,onPlayback:{ playbackStatus = $0 }).frame(maxWidth:.infinity,maxHeight:.infinity)
                Button("播放动态效果（静音）",systemImage:"play.fill") { playbackStatus = "正在请求播放"; playback += 1 }
                if let playbackStatus { Text(playbackStatus).font(.callout).foregroundStyle(.secondary) }
            } else if let image {
                Image(nsImage:image).resizable().scaledToFit().frame(maxWidth:.infinity,maxHeight:.infinity)
            } else if state.busy {
                ProgressView("正在下载并校验预览资源").frame(maxWidth:.infinity,maxHeight:.infinity)
                Text("\(state.receivedBytes.formatted(.byteCount(style:.file))) / \(state.totalBytes.formatted(.byteCount(style:.file)))")
            } else { Spacer() }
            if let error = state.error ?? imageError ?? live.error { Text(error).foregroundStyle(.secondary).fixedSize(horizontal:false,vertical:true) }
            if isLive, !state.urls.isEmpty, live.photo == nil, live.error == nil, imageError == nil {
                ProgressView("正在由系统验证 Live Photo")
            }
            HStack {
                if let url = state.urls.first { Button("原图 Quick Look") { quickLook = url } }
                if state.urls.count == 2 { Button("动态片段 Quick Look") { quickLook = state.urls[1] } }
                Spacer()
                Button(closing ? "正在清理…" : "关闭") {
                    closing = true; quickLook = nil; liveTask?.cancel(); live.clear(); image = nil
                    Task { await state.close(); dismiss() }
                }.keyboardShortcut(.cancelAction).disabled(closing)
            }
        }.padding(20).frame(minWidth:520,idealWidth:680,minHeight:400,idealHeight:540)
        .interactiveDismissDisabled()
        .task { await state.load(request.selection) }
        .onChange(of:state.urls) {
            liveTask?.cancel(); liveTask = nil; image = nil; imageError = nil; playbackStatus = nil; live.clear()
            guard let url = state.urls.first else { return }
            do {
                image = try MediaImageDecoder.thumbnail(url:url)
                if isLive, state.urls.count == 2, let image { let urls = state.urls; liveTask = Task { await live.load(urls,placeholder:image) } }
            }
            catch { imageError = "无法生成有界静态预览，可使用 Quick Look 查看原图。" }
        }
        .quickLookPreview($quickLook)
        .onDisappear { quickLook = nil; liveTask?.cancel(); live.clear(); image = nil; Task { await state.close() } }
    }
}
