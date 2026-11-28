import SwiftUI
import AppKit

struct PhotosView: View {
    let session: SessionState
    let transfers: TransferManager
    @State private var livePhotos = false
    var body: some View {
        GeometryReader { viewport in
            Group {
                if livePhotos { LivePhotosContents(session:session,transfers:transfers) }
                else { PhotoGalleryContents(session:session,transfers:transfers) }
            }
            .frame(width:viewport.size.width,height:viewport.size.height,alignment:.topLeading)
        }
        .navigationTitle("照片")
        .toolbar {
            ToolbarItem(placement:.principal) {
                Picker("照片内容",selection:$livePhotos) {
                    Text("照片").tag(false); Text("Live Photo").tag(true)
                }.pickerStyle(.segmented).frame(width:220)
            }
        }
    }
}

private struct PhotoTile: View {
    let file: FileItem
    let data: Data?
    let error: String?
    let selected: Bool
    @State private var image: NSImage?
    @State private var invalid = false
    var body: some View {
        VStack(spacing:6) {
            Group {
                if let image { Image(nsImage:image).resizable().scaledToFit() }
                else if error != nil || invalid { Image(systemName:"photo.badge.exclamationmark").font(.largeTitle).foregroundStyle(.secondary) }
                else { ProgressView().controlSize(.small) }
            }.frame(height:100).frame(maxWidth:.infinity)
            Text(file.filename).lineLimit(2).frame(height:36)
            Text(file.size.formatted(.byteCount(style:.file))).font(.caption).foregroundStyle(.secondary)
        }.padding(8)
        .background(selected ? Color.accentColor.opacity(0.14) : Color.clear,in:RoundedRectangle(cornerRadius:8))
        .overlay { RoundedRectangle(cornerRadius:8).stroke(selected ? Color.accentColor : .clear,lineWidth:2) }
        .help(error ?? (invalid ? "缩略图格式无法显示，可预览原图。" : file.filename))
        .accessibilityElement(children:.combine)
        .accessibilityLabel("\(file.filename)，\(file.size.formatted(.byteCount(style:.file)))\(error != nil || invalid ? "，缩略图不可用" : "")")
        .task(id:data) {
            image = nil; invalid = false
            guard let data else { return }
            do { image = try MediaImageDecoder.thumbnail(data:data) }
            catch { invalid = true }
        }
        .onDisappear { image = nil }
    }
}

private struct PhotoGalleryContents: View {
    let session: SessionState
    let transfers: TransferManager
    @State private var state: PhotoState
    @State private var name = ""
    @State private var selected: Int64?
    @State private var preview: MediaPreviewRequest?
    @State private var binding: FileItem?
    @State private var inputError: String?

    init(session: SessionState, transfers: TransferManager) {
        self.session = session; self.transfers = transfers
        _state = State(initialValue:PhotoState(session:session))
    }
    private var selectedFile: FileItem? { state.page?.items.first { $0.id == selected } }
    var body: some View {
        VStack(alignment:.leading,spacing:12) {
            if let page = state.page {
                Text("可读取的照片 · 第 \(page.page) 页 · 当前查询 \(page.total) 项").font(.headline)
                Text("每页最多 50 项，按文件名排列；文件或权限变化可能改变后续页面。筛选：\(page.query.name.isEmpty ? "全部名称" : page.query.name)")
                    .font(.callout).foregroundStyle(.secondary).fixedSize(horizontal:false,vertical:true)
                if page.items.isEmpty { ContentUnavailableView("没有匹配的照片",systemImage:"photo") }
                else {
                    ScrollView {
                        LazyVGrid(columns:[GridItem(.adaptive(minimum:140,maximum:220))],spacing:12) {
                            ForEach(page.items) { file in
                                Button { selected = file.id } label: {
                                    PhotoTile(file:file,data:state.thumbnails[file.id],error:state.thumbnailErrors[file.id],selected:selected == file.id)
                                }.buttonStyle(.plain)
                                .simultaneousGesture(TapGesture(count:2).onEnded { preview = MediaPreviewRequest(selection:.photo(file)) })
                                .contextMenu {
                                    Button("预览") { preview = MediaPreviewRequest(selection:.photo(file)) }
                                    Button("下载原图…") { download(file) }
                                    if canBind(file) { Button("关联动态片段…") { binding = file } }
                                }
                            }
                        }.padding(4)
                    }.frame(minHeight:0,maxHeight:.infinity)
                }
                HStack {
                    Button("上一页") { selected = nil; Task { await state.previous() } }.disabled(state.busy || page.page == 1)
                    Button("下一页") { selected = nil; Task { await state.next() } }.disabled(state.busy || !page.hasNext)
                    Spacer()
                }
            } else if state.busy { ProgressView("正在读取照片").frame(maxWidth:.infinity,maxHeight:.infinity) }
            else { Spacer() }
            if let error = inputError ?? state.error ?? transfers.error { Text(error).foregroundStyle(.secondary).fixedSize(horizontal:false,vertical:true) }
        }.padding(16)
        .searchable(text:$name,prompt:"按照片文件名筛选")
        .onSubmit(of:.search) { apply() }
        .toolbar {
            Button("预览照片",systemImage:"eye") { if let file = selectedFile { preview = MediaPreviewRequest(selection:.photo(file)) } }.disabled(selectedFile == nil)
            Button("下载原图…",systemImage:"arrow.down.doc") { if let file = selectedFile { download(file) } }.disabled(selectedFile == nil)
            Button("关联动态片段…",systemImage:"livephoto") { binding = selectedFile }.disabled(selectedFile.map { !canBind($0) } ?? true)
            Button("刷新照片",systemImage:"arrow.clockwise") { selected = nil; Task { await state.load(state.page?.query ?? (try! PhotoQuery())) } }.disabled(state.busy)
        }
        .sheet(item:$preview) { MediaPreviewSheet(request:$0,session:session,transfers:transfers) }
        .sheet(item:$binding) { LivePhotoBindingSheet(session:session,image:$0) }
        .task { await state.load(try! PhotoQuery()) }
        .onDisappear { selected = nil; state.discard() }
    }
    private func canBind(_ file: FileItem) -> Bool { ["jpg","jpeg","heic","heif"].contains((file.filename as NSString).pathExtension.lowercased()) }
    private func apply() {
        do { let query = try PhotoQuery(name:name); inputError = nil; selected = nil; Task { await state.load(query) } }
        catch { inputError = "名称筛选不能超过 200 字节或包含控制字符。" }
    }
    private func download(_ file: FileItem) {
        guard let identity = session.transferIdentity else { return }
        let panel = NSSavePanel(); panel.nameFieldStringValue = file.filename; panel.canCreateDirectories = true
        panel.begin { response in
            guard response == .OK, let url = panel.url else { return }
            Task {
                do { try await transfers.enqueueDownload(file,destination:url,expectedIdentity:identity) }
                catch { transfers.report(error) }
            }
        }
    }
}

private struct LivePhotosContents: View {
    let session: SessionState
    let transfers: TransferManager
    @State private var state: LivePhotoState
    @State private var selected: Int64?
    @State private var unbinding: LivePhotoPair?
    @State private var preview: MediaPreviewRequest?
    init(session: SessionState, transfers: TransferManager) {
        self.session = session; self.transfers = transfers
        _state = State(initialValue:LivePhotoState(session:session))
    }
    private var pair: LivePhotoPair? { state.page?.items.first { $0.id == selected } }
    var body: some View {
        VStack(alignment:.leading,spacing:12) {
            Text("Live Photo 文件关联").font(.headline)
            Text("这里显示可读取的图片与 MOV 配对。预览会校验两个资源并尝试系统解码；建立关联不会重写媒体。解绑保留两个源文件，需要两者写权限。")
                .font(.callout).foregroundStyle(.secondary).fixedSize(horizontal:false,vertical:true)
            if state.busy { ProgressView(state.mutating ? "正在解除关联" : "正在读取 Live Photo") }
            Table(state.page?.items ?? [],selection:$selected) {
                TableColumn("照片") { pair in Text(pair.image.filename) }
                TableColumn("动态片段") { pair in Text(pair.video.filename) }
            }.frame(minHeight:0,maxHeight:.infinity).contextMenu(forSelectionType:Int64.self) { ids in
                if let pair = state.page?.items.first(where:{ids.contains($0.id)}), ids.count == 1 {
                    Button("预览 Live Photo") { preview = MediaPreviewRequest(selection:.livePhoto(pair)) }
                    Button("解除关联…") { unbinding = pair }
                }
            } primaryAction: { ids in
                if let pair = state.page?.items.first(where:{ids.contains($0.id)}), ids.count == 1 { preview = MediaPreviewRequest(selection:.livePhoto(pair)) }
            }
            HStack {
                Button("上一页") { if let page = state.page { selected = nil; Task { await state.load(page:page.page-1) } } }.disabled(state.busy || (state.page?.page ?? 1) == 1)
                Button("下一页") { if let page = state.page { selected = nil; Task { await state.load(page:page.page+1) } } }.disabled(state.busy || state.page?.hasNext != true)
                Spacer()
                if let page = state.page { Text("第 \(page.page) 页 · 当前查询 \(page.total) 项").foregroundStyle(.secondary) }
            }
            if let error = state.error { Text(error).foregroundStyle(.secondary).fixedSize(horizontal:false,vertical:true) }
        }.padding(16)
        .toolbar {
            Button("预览 Live Photo",systemImage:"livephoto") { if let pair { preview = MediaPreviewRequest(selection:.livePhoto(pair)) } }.disabled(state.busy || pair == nil)
            Button("解除关联…",systemImage:"link.badge.plus") { unbinding = pair }.disabled(state.busy || pair == nil)
            Button("刷新 Live Photo",systemImage:"arrow.clockwise") { selected = nil; Task { await state.load() } }.disabled(state.busy)
        }
        .confirmationDialog("解除 Live Photo 关联？",isPresented:Binding(get:{unbinding != nil},set:{if !$0 {unbinding=nil}}),presenting:unbinding) { pair in
            Button("解除关联") { selected = nil; Task { await state.unbind(pair) } }
        } message: { pair in Text("\(pair.image.filename) 与 \(pair.video.filename) 将分别保留，原始文件不会被删除。") }
        .sheet(item:$preview) { MediaPreviewSheet(request:$0,session:session,transfers:transfers) }
        .task { await state.load() }
        .onDisappear { selected = nil; state.discard() }
    }
}

private struct LivePhotoBindingSheet: View {
    @Environment(\.dismiss) private var dismiss
    @State private var state: LivePhotoBindingState
    @State private var name = ""
    @State private var selected: Int64?
    @State private var pending: FileItem?
    @State private var inputError: String?
    init(session: SessionState, image: FileItem) { _state = State(initialValue:LivePhotoBindingState(session:session,image:image)) }
    var body: some View {
        VStack(alignment:.leading,spacing:12) {
            Text("关联动态片段").font(.title2)
            Text("照片：\(state.image.filename)").lineLimit(2)
            Text("选择可读取的 MOV。提交需要两个文件的写权限，不会转换或改写文件；同名资源可能已自动关联，完成后到 Live Photo 页核对。")
                .font(.callout).foregroundStyle(.secondary).fixedSize(horizontal:false,vertical:true)
            if let pair = state.receipt {
                Label("关联已保存",systemImage:"checkmark.circle")
                Text("\(pair.image.filename) · \(pair.video.filename)")
                Spacer()
            } else {
                HStack { TextField("按片段文件名筛选",text:$name).onSubmit { apply() }; Button("筛选") { apply() }.disabled(state.busy || state.locked) }
                List(state.page?.items ?? [],selection:$selected) { file in
                    VStack(alignment:.leading) { Text(file.filename); Text(file.size.formatted(.byteCount(style:.file))).font(.caption).foregroundStyle(.secondary) }.tag(file.id)
                }
                HStack {
                    Button("上一页") { if let page = state.page, let query = state.query { selected = nil; Task { await state.load(query,page:page.page-1) } } }.disabled(state.busy || state.locked || (state.page?.page ?? 1) == 1)
                    Button("下一页") { if let page = state.page, let query = state.query { selected = nil; Task { await state.load(query,page:page.page+1) } } }.disabled(state.busy || state.locked || state.page?.hasNext != true)
                    Spacer()
                    if let page = state.page { Text("当前查询 \(page.total) 项").font(.caption) }
                }
            }
            if let error = inputError ?? state.error { Text(error).foregroundStyle(.secondary).fixedSize(horizontal:false,vertical:true) }
            HStack {
                Button("关闭") { dismiss() }.keyboardShortcut(.cancelAction).disabled(state.submitting)
                Spacer()
                if state.submitting { ProgressView().controlSize(.small) }
                Button("关联…") { pending = state.page?.items.first { $0.id == selected } }.disabled(state.busy || state.locked || selected == nil)
            }
        }.padding(20).frame(width:580,height:520)
        .interactiveDismissDisabled(state.submitting)
        .confirmationDialog("确认关联照片与动态片段？",isPresented:Binding(get:{pending != nil},set:{if !$0 {pending=nil}}),presenting:pending) { video in
            Button("确认关联") { Task { await state.bind(video) } }
        } message: { video in Text("\(state.image.filename) 与 \(video.filename)。这只保存文件关联，不保证媒体包含有效的 Live Photo 信息。") }
        .task { await state.load(try! PhotoQuery()) }
        .onDisappear { state.discard() }
    }
    private func apply() {
        do { let query = try PhotoQuery(name:name); inputError = nil; selected = nil; Task { await state.load(query) } }
        catch { inputError = "请缩短文件名筛选并移除控制字符。" }
    }
}
