import Foundation
import Testing
@testable import CloudNestCore

@Test func transferSpeedUsesBodyFlowAndDecaysDuringAStall() throws {
    let start = ContinuousClock.now
    let meter = TransferTelemetry(at: start)
    meter.record(bytes: 999, at: start)
    meter.transition(to: .checking, at: start)
    meter.record(bytes: 999, at: start)
    #expect(meter.snapshot(at: start).bodyBytes == 0)
    meter.transition(to: .transferring, at: start)
    meter.record(bytes: 1000, at: start.advanced(by: .milliseconds(100)))
    #expect(meter.snapshot(at: start.advanced(by: .milliseconds(200))).bytesPerSecond == nil)
    #expect(meter.snapshot(at: start.advanced(by: .seconds(1))).bytesPerSecond == 1000)
    #expect(meter.snapshot(at: start.advanced(by: .seconds(2))).bytesPerSecond == 500)
    #expect(meter.snapshot(at: start.advanced(by: .seconds(6))).bytesPerSecond == 0)
    #expect(meter.snapshot(at: start.advanced(by: .seconds(6))).bodyBytes == 1000)
    meter.transition(to: .confirming, at: start.advanced(by: .seconds(6)))
    meter.record(bytes: 999, at: start.advanced(by: .seconds(7)))
    #expect(meter.snapshot(at: start.advanced(by: .seconds(7))).bytesPerSecond == nil)
    #expect(meter.snapshot(at: start.advanced(by: .seconds(7))).bodyBytes == 1000)
}

@Test func transferSpeedResetsForNewRepresentationsAndStopsLateCallbacks() {
    let start = ContinuousClock.now
    let meter = TransferTelemetry(at: start)
    meter.transition(to: .transferring, at: start)
    meter.record(bytes: 4096, at: start.advanced(by: .seconds(1)))
    meter.restartRepresentation(at: start.advanced(by: .seconds(2)))
    #expect(meter.snapshot(at: start.advanced(by: .seconds(2))).bodyBytes == 0)
    meter.record(bytes: 512, at: start.advanced(by: .seconds(3)))
    #expect(meter.snapshot(at: start.advanced(by: .seconds(3))).bytesPerSecond == 512)
    meter.stop()
    meter.record(bytes: 999, at: start.advanced(by: .seconds(4)))
    meter.transition(to: .transferring, at: start.advanced(by: .seconds(4)))
    #expect(meter.snapshot(at: start.advanced(by: .seconds(4))).bodyBytes == 512)
    #expect(meter.snapshot(at: start.advanced(by: .seconds(4))).bytesPerSecond == nil)
    let resumed = TransferTelemetry(at: start.advanced(by: .seconds(4)))
    resumed.transition(to: .transferring, at: start.advanced(by: .seconds(4)))
    resumed.record(bytes: 128, at: start.advanced(by: .seconds(5)))
    #expect(resumed.snapshot(at: start.advanced(by: .seconds(5))).bytesPerSecond == 128)
}

@Test func transferSpeedBoundsSamplesAndHandlesExtremeCounts() {
    let start = ContinuousClock.now
    let meter = TransferTelemetry(at: start)
    meter.transition(to: .transferring, at: start)
    for index in 0..<10_000 {
        let instant = start.advanced(by: .milliseconds(index))
        meter.record(bytes: 100, at: instant)
        #expect(meter.snapshot(at: instant).bufferedSamples <= 27)
    }
    meter.record(bytes: -1, at: start.advanced(by: .seconds(10)))
    meter.record(bytes: 100, at: start) // A stale timestamp cannot reorder the window.
    #expect(meter.snapshot(at: start.advanced(by: .seconds(10))).bodyBytes == 1_000_000)
    meter.record(bytes: Int64.max, at: start.advanced(by: .seconds(11)))
    meter.record(bytes: Int64.max, at: start.advanced(by: .seconds(11)))
    let snapshot = meter.snapshot(at: start.advanced(by: .seconds(11)))
    #expect(snapshot.bodyBytes == Int64.max)
    #expect(snapshot.bytesPerSecond?.isFinite == true)
    #expect(TransferTelemetry.formattedRate(.infinity) == "—")
    #expect(TransferTelemetry.formattedRate(.nan) == "—")
    #expect(TransferTelemetry.formattedRate(-1) == "—")
    #expect(!TransferTelemetry.formattedRate(Double(Int64.max)).isEmpty)
}

@Test func transferSpeedSerializesConcurrentDelegateCallbacks() {
    let meter = TransferTelemetry()
    meter.transition(to: .transferring)
    DispatchQueue.concurrentPerform(iterations: 10_000) { _ in
        meter.record(bytes: 100)
        _ = meter.snapshot()
    }
    #expect(meter.snapshot().bodyBytes == 1_000_000)
    #expect(meter.snapshot().bufferedSamples <= 27)
}
