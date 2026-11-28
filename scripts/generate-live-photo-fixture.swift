// Deterministic synthetic media for opt-in native acceptance, never a Photos
// library import. The resulting pair must pass the real PHLivePhoto decoder.
import Foundation
import AppKit
import CoreGraphics
import CoreVideo
import CoreMedia
import AVFoundation
import ImageIO
import Photos
import UniformTypeIdentifiers

private enum FixtureError: Error { case invalidDirectory, encoding, timeout, livePhotoDecode }
private final class LivePhotoProbe: @unchecked Sendable {
    private let lock = NSLock()
    private var finished = false
    private var success = false
    private var callbacks = 0
    private var degradedCallbacks = 0
    private var errorDomain = ""
    private var errorCode = 0
    private var events: [[String:Any]] = []
    func accept(_ photo: PHLivePhoto?, _ info: [AnyHashable:Any]) {
        let degraded = info[PHLivePhotoInfoIsDegradedKey] as? Bool ?? false
        lock.lock(); defer { lock.unlock() }
        callbacks += 1
        events.append(["has_photo":photo != nil,"width":photo?.size.width ?? -1,"height":photo?.size.height ?? -1,
            "degraded":degraded,"cancelled":info[PHLivePhotoInfoCancelledKey] as? Bool ?? false,
            "resources":photo.map { PHAssetResource.assetResources(for:$0).map { ["type":$0.type.rawValue,"name":$0.originalFilename,"width":$0.pixelWidth,"height":$0.pixelHeight] as [String:Any] } } ?? [],
            "info_keys":info.keys.map { String(describing:$0) }.sorted()])
        if degraded { degradedCallbacks += 1 }
        if let error = info[PHLivePhotoInfoErrorKey] as? NSError { errorDomain = error.domain; errorCode = error.code }
        guard !degraded else { return }
        guard !finished else { return }
        if let photo {
            let types = PHAssetResource.assetResources(for:photo).map(\.type)
            success = photo.size.width > 0 && photo.size.height > 0 && types.count == 2 && types.contains(.photo) && types.contains(.pairedVideo)
                && info[PHLivePhotoInfoErrorKey] == nil && (info[PHLivePhotoInfoCancelledKey] as? Bool ?? false) == false
            finished = true
        }
        else if info[PHLivePhotoInfoErrorKey] != nil { finished = true }
    }
    var result: (Bool,Bool) { lock.lock(); defer { lock.unlock() }; return (finished,success) }
    var diagnostics: [String:Any] {
        lock.lock(); defer { lock.unlock() }
        return ["callbacks":callbacks,"degraded_callbacks":degradedCallbacks,"finished":finished,"success":success,"error_domain":errorDomain,"error_code":errorCode,"events":events]
    }
}

@main
private enum GenerateLivePhotoFixture {
    static func pixels(frame: Int) throws -> CVPixelBuffer {
        var pixel: CVPixelBuffer?
        guard CVPixelBufferCreate(kCFAllocatorDefault,320,240,kCVPixelFormatType_32BGRA,
            [kCVPixelBufferCGImageCompatibilityKey:true,kCVPixelBufferCGBitmapContextCompatibilityKey:true] as CFDictionary,&pixel) == kCVReturnSuccess,
              let pixel else { throw FixtureError.encoding }
        CVPixelBufferLockBaseAddress(pixel,[])
        defer { CVPixelBufferUnlockBaseAddress(pixel,[]) }
        guard let context = CGContext(data:CVPixelBufferGetBaseAddress(pixel),width:320,height:240,bitsPerComponent:8,
            bytesPerRow:CVPixelBufferGetBytesPerRow(pixel),space:CGColorSpaceCreateDeviceRGB(),
            bitmapInfo:CGImageAlphaInfo.premultipliedFirst.rawValue | CGBitmapInfo.byteOrder32Little.rawValue) else { throw FixtureError.encoding }
        context.setFillColor(CGColor(red:0.12,green:0.48,blue:0.85,alpha:1));context.fill(CGRect(x:0,y:0,width:320,height:240))
        context.setFillColor(CGColor(red:0.93,green:0.24,blue:0.12,alpha:1));context.fill(CGRect(x:20+frame*4,y:80,width:70,height:70))
        context.setFillColor(CGColor(gray:0.9,alpha:1));context.fill(CGRect(x:0,y:0,width:320,height:40))
        return pixel
    }
    static func main() {
        do { try run() }
        catch { FileHandle.standardError.write(Data("Synthetic media fixture failed: \(error)\n".utf8)); exit(1) }
    }
    static func verify(_ urls: [URL]) throws {
        let probe = LivePhotoProbe()
        let size = ProcessInfo.processInfo.environment["CLOUDNEST_LIVE_PHOTO_ORIGINAL_SIZE"] == "1" ? CGSize.zero : CGSize(width:320,height:240)
        let placeholder = ProcessInfo.processInfo.environment["CLOUDNEST_LIVE_PHOTO_NO_PLACEHOLDER"] == "1" ? nil : NSImage(contentsOf:urls[0])
        let request = PHLivePhoto.request(withResourceFileURLs:urls,placeholderImage:placeholder,targetSize:size,contentMode:.aspectFit) { photo,info in probe.accept(photo,info) }
        let decodeDeadline = Date().addingTimeInterval(15)
        while !probe.result.0 && Date() < decodeDeadline { RunLoop.current.run(until:Date().addingTimeInterval(0.02)) }
        PHLivePhoto.cancelRequest(withRequestID:request)
        let diagnostic = try JSONSerialization.data(withJSONObject:probe.diagnostics,options:[.sortedKeys])
        print(String(decoding:diagnostic,as:UTF8.self))
        guard probe.result.1 else { throw FixtureError.livePhotoDecode }
    }
    static func run() throws {
        if CommandLine.arguments.count == 4, CommandLine.arguments[1] == "--verify" {
            try verify(CommandLine.arguments.dropFirst(2).map { URL(fileURLWithPath:$0) })
            return
        }
        guard CommandLine.arguments.count == 2 else { throw FixtureError.invalidDirectory }
        let root = URL(fileURLWithPath:CommandLine.arguments[1]).standardizedFileURL
        let fm = FileManager.default
        guard root.lastPathComponent.hasPrefix("cloudnest-native-media-"),
              ["/tmp","/private/tmp"].contains(root.deletingLastPathComponent().path),
              try root.resourceValues(forKeys:[.isDirectoryKey,.isSymbolicLinkKey]).isDirectory == true,
              try root.resourceValues(forKeys:[.isSymbolicLinkKey]).isSymbolicLink != true,
              try fm.contentsOfDirectory(atPath:root.path).isEmpty else { throw FixtureError.invalidDirectory }
        try fm.setAttributes([.posixPermissions:0o700],ofItemAtPath:root.path)
        let identifier = UUID().uuidString
        let jpeg = root.appending(path:"Synthetic Live.jpg"), movie = root.appending(path:"Synthetic Live.mov")
        let still = try pixels(frame:22)
        CVPixelBufferLockBaseAddress(still,[])
        guard let context = CGContext(data:CVPixelBufferGetBaseAddress(still),width:320,height:240,bitsPerComponent:8,
            bytesPerRow:CVPixelBufferGetBytesPerRow(still),space:CGColorSpaceCreateDeviceRGB(),
            bitmapInfo:CGImageAlphaInfo.premultipliedFirst.rawValue | CGBitmapInfo.byteOrder32Little.rawValue),
              let image = context.makeImage(),
              let destination = CGImageDestinationCreateWithURL(jpeg as CFURL,UTType.jpeg.identifier as CFString,1,nil) else { throw FixtureError.encoding }
        CGImageDestinationAddImage(destination,image,[kCGImagePropertyMakerAppleDictionary:["17":identifier],
            kCGImagePropertyExifDictionary:[kCGImagePropertyExifDateTimeOriginal:"2025:07:14 12:34:56"],
            kCGImageDestinationLossyCompressionQuality:0.9] as CFDictionary)
        guard CGImageDestinationFinalize(destination) else { throw FixtureError.encoding }
        let png = root.appending(path:"Synthetic static.png")
        guard let pngDestination = CGImageDestinationCreateWithURL(png as CFURL,UTType.png.identifier as CFString,1,nil) else { throw FixtureError.encoding }
        CGImageDestinationAddImage(pngDestination,image,nil)
        guard CGImageDestinationFinalize(pngDestination) else { throw FixtureError.encoding }
        CVPixelBufferUnlockBaseAddress(still,[])
        let writer = try AVAssetWriter(outputURL:movie,fileType:.mov)
        let content = AVMutableMetadataItem(); content.identifier = .quickTimeMetadataContentIdentifier
        content.value = identifier as NSString; content.dataType = kCMMetadataBaseDataType_UTF8 as String
        writer.metadata = [content]
        let input = AVAssetWriterInput(mediaType:.video,outputSettings:[AVVideoCodecKey:AVVideoCodecType.h264,AVVideoWidthKey:320,AVVideoHeightKey:240])
        let adaptor = AVAssetWriterInputPixelBufferAdaptor(assetWriterInput:input,sourcePixelBufferAttributes:[kCVPixelBufferPixelFormatTypeKey as String:kCVPixelFormatType_32BGRA,kCVPixelBufferWidthKey as String:320,kCVPixelBufferHeightKey as String:240])
        writer.add(input)
        var format: CMFormatDescription?
        let specification: [String:Any] = [kCMMetadataFormatDescriptionMetadataSpecificationKey_Identifier as String:"mdta/com.apple.quicktime.still-image-time",kCMMetadataFormatDescriptionMetadataSpecificationKey_DataType as String:kCMMetadataBaseDataType_SInt8]
        guard CMMetadataFormatDescriptionCreateWithMetadataSpecifications(allocator:kCFAllocatorDefault,metadataType:kCMMetadataFormatType_Boxed,metadataSpecifications:[specification] as CFArray,formatDescriptionOut:&format) == noErr else { throw FixtureError.encoding }
        let timedInput = AVAssetWriterInput(mediaType:.metadata,outputSettings:nil,sourceFormatHint:format)
        let timed = AVAssetWriterInputMetadataAdaptor(assetWriterInput:timedInput); writer.add(timedInput)
        guard writer.startWriting() else { throw FixtureError.encoding }
        writer.startSession(atSourceTime:.zero)
        let item = AVMutableMetadataItem(); item.identifier = AVMetadataIdentifier(rawValue:"mdta/com.apple.quicktime.still-image-time")
        item.dataType = kCMMetadataBaseDataType_SInt8 as String; item.value = NSNumber(value:Int8(0))
        guard timed.append(AVTimedMetadataGroup(items:[item],timeRange:CMTimeRange(start:CMTime(value:22,timescale:15),duration:CMTime(value:1,timescale:15)))) else { throw FixtureError.encoding }
        timedInput.markAsFinished()
        let deadline = Date().addingTimeInterval(30)
        for frame in 0..<45 {
            while !input.isReadyForMoreMediaData { guard Date() < deadline else { throw FixtureError.timeout }; Thread.sleep(forTimeInterval:0.005) }
            guard adaptor.append(try pixels(frame:frame),withPresentationTime:CMTime(value:Int64(frame),timescale:15)) else { throw FixtureError.encoding }
        }
        input.markAsFinished(); writer.endSession(atSourceTime:CMTime(value:3,timescale:1))
        let done = DispatchSemaphore(value:0); writer.finishWriting { done.signal() }
        guard done.wait(timeout:.now()+15) == .success, writer.status == .completed else { throw FixtureError.encoding }
        try verify([jpeg,movie])
        for url in [jpeg,movie,png] { try fm.setAttributes([.posixPermissions:0o600],ofItemAtPath:url.path) }
        print("Synthetic JPEG/MOV pair decoded by PHLivePhoto; 320x240, 3 seconds, no Photos library import.")
    }
}
