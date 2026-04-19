import SwiftUI

struct TestView: View {
    var body: some View {
        Text("Hello")
            .contextMenu {
                Button("Action") {}
            } preview: {
                Image(systemName: "star")
            }
    }
}
