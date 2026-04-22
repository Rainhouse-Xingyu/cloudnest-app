extension FilesViewModel {
    func resolveLivePhotoPreview(for id: Int64) async throws -> (URL, URL)? {
        guard let cached = livePhotoCache[id], let info = cached else {
            return nil
        }
        let iURL = try await service.downloadPreview(fileId: info.imageFileId, filename: info.imageFile.filename)
        let vURL = try await service.downloadPreview(fileId: info.videoFileId, filename: info.videoFile.filename)
        return (iURL, vURL)
    }
    
    func resolveImagePreview(for id: Int64, filename: String) async throws -> URL {
        return try await service.downloadPreview(fileId: id, filename: filename)
    }
}
