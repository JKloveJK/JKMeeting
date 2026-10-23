import Foundation
import Synchronization

/// 单生产者单消费者的 Float 环形缓冲：解码线程写，音频渲染线程读。
/// 读写两端只通过原子计数同步，渲染路径上没有锁和内存分配。
final class PCMRingBuffer: @unchecked Sendable {
    let capacity: Int
    private let storage: UnsafeMutablePointer<Float>
    private let written = Atomic<Int>(0)  // 累计写入样本数，只由生产者递增
    private let read = Atomic<Int>(0)  // 累计读取样本数，只由消费者递增

    init(capacity: Int) {
        precondition(capacity > 0)
        self.capacity = capacity
        storage = .allocate(capacity: capacity)
        storage.initialize(repeating: 0, count: capacity)
    }

    deinit {
        storage.deallocate()
    }

    var availableToRead: Int { written.load(ordering: .acquiring) - read.load(ordering: .relaxed) }
    var availableToWrite: Int { capacity - (written.load(ordering: .relaxed) - read.load(ordering: .acquiring)) }

    /// 只写入当前空闲的部分，返回实际写入的样本数。
    @discardableResult
    func write(_ samples: UnsafeBufferPointer<Float>) -> Int {
        let count = min(samples.count, availableToWrite)
        guard count > 0, let base = samples.baseAddress else { return 0 }
        let start = written.load(ordering: .relaxed) % capacity
        let first = min(count, capacity - start)
        (storage + start).update(from: base, count: first)
        if count > first {
            storage.update(from: base + first, count: count - first)
        }
        written.add(count, ordering: .releasing)
        return count
    }

    /// 最多读取 `count` 个样本，返回实际读取数。
    func read(into destination: UnsafeMutablePointer<Float>, count: Int) -> Int {
        let available = min(count, availableToRead)
        guard available > 0 else { return 0 }
        let start = read.load(ordering: .relaxed) % capacity
        let first = min(available, capacity - start)
        destination.update(from: storage + start, count: first)
        if available > first {
            (destination + first).update(from: storage, count: available - first)
        }
        read.add(available, ordering: .releasing)
        return available
    }
}
