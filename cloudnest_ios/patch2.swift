struct ContextMenuLivePreview: View {
    let item: FileItemDTO
    @ObservedObject var viewModel: FilesViewModel
    
    @State private var imageURL: URL?
    @State private var videoURL: URL?
    @State private var isLoading = true

    var body: some View {
        Group {
            if viewModel.isLivePhoto(item.id) {
                if let iURL = imageURL, let vURL = videoURL {
                    LivePhotoDisplayView(imageURL: iURL, videoURL: vURL)
                } else {
                    loadingOrThumb()
                }
            } else if viewModel.thumbnail(for: item.id) != nil {
                if let iURL = imageURL, let uiImage = UIImage(contentsOfFile: iURL.path) {
                    Image(uiImage: uiImage)
                        .resizable()
                        .scaledToFit()
                } else {
                    loadingOrThumb()
                }
            } else {
                Image(systemName: getFileIcon(type: item.fileType))
                    .font(.system(size: 60))
                    .padding()
            }
        }
        .frame(minWidth: 200, minHeight: 200)
        .task {
            await loadRealContent()
        }
    }

    @ViewBuilder
    private func loadingOrThumb() -> some View {
        ZStack {
            if let thumb = viewModel.thumbnail(for: item.id) {
                Image(uiImage: thumb)
                    .resizable()
                    .scaledToFit()
                    .blur(radius: 2)
            } else {
                Color.gray.opacity(0.1)
            }
            ProgressView()
        }
    }

    private func loadRealContent() async {
        defer { isLoading = false }
        if viewModel.isLivePhoto(item.id) {
            guard let cached = viewModel.livePhotoCache[item.id], let info = cached else { return }
            do {
                async let iURLTask = viewModel.service.downloadPreview(fileId: info.imageFileId, filename: info.imageFile.filename)
                async let vURLTask = viewModel.service.downloadPreview(fileId: info.videoFileId, filename: info.videoFile.filename)
                let (iURL, vURL) = try await (iURLTask, vURLTask)
                
                if !Task.isCancelled {
                    imageURL = iURL
                    videoURL = vURL
                }
            } catch {
                 print("Failed Live Photo preview: \(error)")
            }
        } else if viewModel.thumbnail(for: item.id) != nil {
            do {
                let url = try await viewModel.service.downloadPreview(fileId: item.id, filename: item.filename)
                if !Task.isCancelled {
                    imageURL = url
                }
            } catch {
                 print("Failed image preview: \(error)")
            }
        }
    }
    
    private func getFileIcon(type: String) -> String {
        let typeLower = type.lowercased()
        if typeLower.contains("pdf") { return "doc.richtext.fill" }
        if typeLower.contains("image") || typeLower.contains("jpg") || typeLower.contains("png") || typeLower.contains("heic") { return "photo.fill" }
        if typeLower.contains("video") || typeLower.contains("mp4") || typeLower.contains("mov") { return "play.rectangle.fill" }
        if typeLower.contains("audio") || typeLower.contains("mp3") { return "headphones" }
        if typeLower.contains("text") || typeLower.contains("txt") || typeLower.contains("md") { return "doc.text.fill" }
        return "doc.fill"
    }
}
