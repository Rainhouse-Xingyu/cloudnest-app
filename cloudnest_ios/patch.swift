struct ContextMenuPreviewLoader: View {
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
                } else if isLoading {
                    ProgressView()
                } else {
                    ContentErrorView()
                }
            } else if isImage(item.fileType) {
                if let imageURL, let uiImg = UIImage(contentsOfFile: imageURL.path) {
                    Image(uiImage: uiImg)
                        .resizable()
                        .scaledToFit()
                } else if isLoading {
                    ProgressView()
                } else {
                    ContentErrorView()
                }
            } else {
                FallbackView()
            }
        }
        .frame(minWidth: 200, minHeight: 200)
        .onAppear {
            Task {
                await loadRealContent()
            }
        }
    }
}
