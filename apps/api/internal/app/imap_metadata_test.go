package app

import (
	"context"
	"sync"
	"testing"
)

// 批量删除时前端会并发发 4 个 DELETE，每个都要从 folders 里领一个新 UID。
// UID 分配是「读 uid_next → 写回 +1 → 把读到的值写进 messages」三步，
// 并发时两个请求会读到同一个值，第二封邮件写 messages 时撞唯一索引，
// 于是整批报「批量操作失败」。这个测试锁住「并发领取的 UID 两两不同」。
func TestNextIMAPMetadataConcurrentAllocationsAreUnique(t *testing.T) {
	a, _, mailbox := newAdminPaginationTest(t)
	ctx := context.Background()
	folderID, err := a.ensureFolder(ctx, mailbox.ID, "Trash")
	if err != nil {
		t.Fatalf("建 Trash 失败: %v", err)
	}

	const n = 32
	uids := make([]int64, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			meta, err := a.nextIMAPMetadata(ctx, a.db, folderID)
			uids[i], errs[i] = meta.UID, err
		}(i)
	}
	wg.Wait()

	seen := make(map[int64]bool, n)
	for i, err := range errs {
		if err != nil {
			t.Fatalf("第 %d 个领取失败: %v", i, err)
		}
	}
	for i, uid := range uids {
		if uid < 1 {
			t.Fatalf("第 %d 个拿到了非法 UID %d", i, uid)
		}
		if seen[uid] {
			t.Fatalf("UID %d 被分配了两次（并发读改写）", uid)
		}
		seen[uid] = true
	}
}
