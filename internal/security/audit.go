package security

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

const (
	// auditBucket 是审计事件唯一的 bucket（API.md 第 9 节）。
	auditBucket = "audit"
	// auditMemoryCap 是内存兜底的最近事件条数。
	auditMemoryCap = 512
	// auditListMax 是 List 单次返回上限。
	auditListMax  = 500
	auditSeqWidth = 16
)

// Audit 是 append-only 审计日志：bucket=audit，key 为 16 位递增序号。
// 写入只追加、不修改旧记录；List 按序号倒序返回最近 N 条。
type Audit struct {
	store contract.Store

	mu     sync.Mutex
	seq    int64
	loaded bool
	mem    []contract.AuditEvent
}

var _ contract.Audit = (*Audit)(nil)

// NewAudit 构造审计日志。store 为 nil 时退化为纯内存模式（仅测试/降级用）。
func NewAudit(store contract.Store) contract.Audit {
	return &Audit{store: store}
}

func auditKey(seq int64) string { return fmt.Sprintf("%0*d", auditSeqWidth, seq) }

// Record 追加一条审计事件。失败不阻塞业务：落盘失败时仍保留在内存兜底里。
func (a *Audit) Record(e contract.AuditEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.ensureLoadedLocked()
	a.seq++
	buf, err := json.Marshal(e)
	if err == nil && a.store != nil {
		_ = a.store.Put(auditBucket, auditKey(a.seq), buf)
	}
	a.mem = append(a.mem, e)
	if len(a.mem) > auditMemoryCap {
		a.mem = a.mem[len(a.mem)-auditMemoryCap:]
	}
}

// List 返回最近 limit 条审计事件，倒序（最新在前）。
func (a *Audit) List(limit int) ([]contract.AuditEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > auditListMax {
		limit = auditListMax
	}

	a.mu.Lock()
	store := a.store
	a.mu.Unlock()

	if store != nil {
		items, err := store.List(auditBucket, "")
		if err == nil {
			sort.Slice(items, func(i, j int) bool { return items[i].Key > items[j].Key })
			out := make([]contract.AuditEvent, 0, limit)
			for _, it := range items {
				if len(out) >= limit {
					break
				}
				var e contract.AuditEvent
				if err := json.Unmarshal(it.Value, &e); err != nil {
					continue // 损坏记录跳过，不影响其它事件
				}
				out = append(out, e)
			}
			return out, nil
		}
	}

	// 存储不可用时用内存兜底，保证设置页至少有近期审计可看。
	a.mu.Lock()
	defer a.mu.Unlock()
	n := len(a.mem)
	if n > limit {
		n = limit
	}
	out := make([]contract.AuditEvent, 0, n)
	for i := len(a.mem) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, a.mem[i])
	}
	return out, nil
}

// ensureLoadedLocked 在首次写入前把序号对齐到存储里的最大序号，避免重启后覆盖旧记录。
func (a *Audit) ensureLoadedLocked() {
	if a.loaded {
		return
	}
	a.loaded = true
	if a.store == nil {
		return
	}
	items, err := a.store.List(auditBucket, "")
	if err != nil {
		return
	}
	for _, it := range items {
		var seq int64
		if _, err := fmt.Sscanf(it.Key, "%d", &seq); err == nil && seq > a.seq {
			a.seq = seq
		}
	}
}
