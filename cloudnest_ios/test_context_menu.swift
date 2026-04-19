import SwiftUI

struct TestView: View {
    var body: some View {
        Text("Hello")
            .contextMenu(menuItems: {
                Button("Action") {}
            }, preview: {
                Image(systemName: "star")
            })
    }
}
