package httpapi

import (
	"testing"
	"time"
)

func TestStreamTrackerCountsLossAndReorder(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	tracker := newStreamTracker(base)
	// 0,1,3（丢 2）,2（迟到重排）,4,4（重复）
	for offset, seq := range []uint32{0, 1, 3, 2, 4, 4} {
		tracker.observe(seq, base.Add(time.Duration(offset*20)*time.Millisecond))
	}
	stats := tracker.finish(base.Add(200 * time.Millisecond))
	if stats.Received != 5 {
		t.Fatalf("received=%d，期望 5", stats.Received)
	}
	if stats.LostEstimated != 0 {
		t.Fatalf("迟到帧到达后 lost 应回补为 0，实际 %d", stats.LostEstimated)
	}
	if stats.Reordered != 1 || stats.Duplicates != 1 {
		t.Fatalf("reordered=%d duplicates=%d，期望 1/1", stats.Reordered, stats.Duplicates)
	}
	if stats.FirstFrameMS != 0 {
		t.Fatalf("first_frame_ms=%d，期望 0（首帧与建连同毫秒口径）", stats.FirstFrameMS)
	}
}

func TestStreamTrackerEstimatesPersistentLoss(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	tracker := newStreamTracker(base)
	for _, seq := range []uint32{10, 11, 14, 15} { // 12、13 真丢失
		tracker.observe(seq, base)
	}
	stats := tracker.finish(base)
	if stats.LostEstimated != 2 {
		t.Fatalf("lost=%d，期望 2", stats.LostEstimated)
	}
}
