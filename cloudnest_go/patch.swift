extension FilesView {
    @ViewBuilder
    private func contextMenuPreview(for item: FileItemDTO) -> some View {
        if let thumb = viewModel.thumbnail(for: item.id) {
            Image(uiImage: thumb)
                .resizable()
                .scaledToFit()
        } else {
            Image(systemName: getFileIcon(type: item.fileType))
                .font(.system(size: 60))
                .padding()
        }
    }
}
