package progressive

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yazzyk/stock-agent/pkg/contract"
	"github.com/yazzyk/stock-agent/pkg/plugin"
)

// nl 是换行符（用 rune 构造，避免测试源码里出现转义序列）。
var nl = string(rune(10))

type memStore struct {
	mu   sync.RWMutex
	data map[string]map[string][]byte
}

func newMemStore() *memStore { return &memStore{data: map[string]map[string][]byte{}} }

func (s *memStore) Get(bucket, key string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if b, ok := s.data[bucket]; ok {
		if v, ok := b[key]; ok {
			return v, nil
		}
	}
	return nil, contract.ErrNotFound
}

func (s *memStore) Put(bucket, key string, val []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data[bucket] == nil {
		s.data[bucket] = map[string][]byte{}
	}
	s.data[bucket][key] = append([]byte{}, val...)
	return nil
}

func (s *memStore) Delete(bucket, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data[bucket], key)
	return nil
}

func (s *memStore) List(bucket, prefix string) ([]contract.KVItem, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]contract.KVItem, 0, len(s.data[bucket]))
	for k, v := range s.data[bucket] {
		if strings.HasPrefix(k, prefix) {
			out = append(out, contract.KVItem{Key: k, Value: append([]byte{}, v...)})
		}
	}
	return out, nil
}

func (s *memStore) Close() error { return nil }

func mustImport(t *testing.T, kb *KB, title, content string) ImportResult {
	t.Helper()
	res, err := kb.Import(context.Background(), ImportInput{Title: title, Content: content})
	if err != nil {
		t.Fatalf("导入 %s 失败: %v", title, err)
	}
	return res
}

func seedKB(t *testing.T) *KB {
	t.Helper()
	kb := NewKB(newMemStore())
	mustImport(t, kb, "涨停板战法", `# 涨停板战法

涨停板战法的核心是分歧转一致。首板涨停后先看封单强度。
涨停 涨停 分歧转一致 才是可以参与的短线买点。`)
	mustImport(t, kb, "资金流判读", `# 资金流判读

主力资金净流入与超大单净额是判断资金进出的关键。
北向资金连续流入时，题材持续性更好。`)
	mustImport(t, kb, "复盘流程", `# 复盘流程

每天收盘后复盘涨停家数、连板高度与情绪周期。
记录炸板率与昨日涨停今天的溢价。`)
	return kb
}

func TestKBImportAndSearchTop1(t *testing.T) {
	kb := seedKB(t)
	res, err := kb.Search(context.Background(), "涨停", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) < 2 {
		t.Fatalf("“涨停”应命中至少 2 篇，实际 %d", len(res.Hits))
	}
	if res.Hits[0].Title != "涨停板战法" {
		t.Fatalf("top1 应为《涨停板战法》，实际 %q（分数 %v）", res.Hits[0].Title, res.Hits[0].Score)
	}
	if res.Hits[0].Score <= res.Hits[1].Score {
		t.Fatalf("分数必须严格递减: %v <= %v", res.Hits[0].Score, res.Hits[1].Score)
	}
	if !strings.Contains(res.Hits[0].Snippet, "涨停") {
		t.Fatalf("片段应包含查询词: %q", res.Hits[0].Snippet)
	}
	if len([]rune(res.Hits[0].Snippet)) > kbSnippetLimit+2 {
		t.Fatalf("片段不能超过 %d 字: %d", kbSnippetLimit, len([]rune(res.Hits[0].Snippet)))
	}

	res2, err := kb.Search(context.Background(), "北向资金", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(res2.Hits) == 0 || res2.Hits[0].Title != "资金流判读" {
		t.Fatalf("“北向资金”top1 应为《资金流判读》，实际 %+v", res2.Hits)
	}
}

func TestKBSearchDeterministic(t *testing.T) {
	kb1 := seedKB(t)
	kb2 := seedKB(t)
	q := "涨停 分歧转一致"
	a, err := kb1.Search(context.Background(), q, 10)
	if err != nil {
		t.Fatal(err)
	}
	b, err := kb1.Search(context.Background(), q, 10)
	if err != nil {
		t.Fatal(err)
	}
	c, err := kb2.Search(context.Background(), q, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Hits) != len(b.Hits) || len(a.Hits) != len(c.Hits) {
		t.Fatalf("同样输入命中数必须一致: %d / %d / %d", len(a.Hits), len(b.Hits), len(c.Hits))
	}
	for i := range a.Hits {
		if a.Hits[i].ID != b.Hits[i].ID || a.Hits[i].Score != b.Hits[i].Score {
			t.Fatalf("同一实例两次检索必须完全一致: %+v vs %+v", a.Hits[i], b.Hits[i])
		}
		if a.Hits[i].ID != c.Hits[i].ID || a.Hits[i].Score != c.Hits[i].Score {
			t.Fatalf("不同实例同输入必须同样打分: %+v vs %+v", a.Hits[i], c.Hits[i])
		}
	}
}

func TestKBEmptyQueryAndEmptyKB(t *testing.T) {
	kb := NewKB(newMemStore())
	if !kb.IsEmpty() {
		t.Fatal("新建知识库应为空")
	}
	res, err := kb.Search(context.Background(), "   ", 0)
	if err != nil {
		t.Fatalf("空 query 不应报错（验收用 {} 直接跑）: %v", err)
	}
	if len(res.Hits) != 0 || !strings.Contains(res.Hint, "查询词") {
		t.Fatalf("空 query 应返回空列表 + 中文提示，实际: %+v", res)
	}
	res2, err := kb.Search(context.Background(), "涨停", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res2.Hits) != 0 || res2.Hint == "" {
		t.Fatalf("空知识库应给提示而不是报错: %+v", res2)
	}
	if _, err := kb.Open(context.Background(), "nope"); err == nil {
		t.Fatal("打开不存在的 id 必须报错")
	}
}

func TestKBChunkingCoverage(t *testing.T) {
	text := strings.Repeat("涨停后观察封单与量能变化。", 150)
	chunks := chunkText(text)
	if len(chunks) < 3 {
		t.Fatalf("约 %d 字的文本应切成至少 3 块，实际 %d", len([]rune(text)), len(chunks))
	}
	if !strings.HasPrefix(chunks[0], "涨停后观察封单") {
		t.Fatalf("首块应从文档开头开始: %q", chunks[0][:min(12, len(chunks[0]))])
	}
	last := chunks[len(chunks)-1]
	if !strings.HasSuffix(last, "量能变化。") {
		t.Fatalf("末块应覆盖到文档结尾: %q", last)
	}
	for i, c := range chunks {
		n := len([]rune(c))
		if n > kbChunkRunes+60 {
			t.Fatalf("第 %d 块过长: %d 字", i, n)
		}
		if i > 0 {
			prev := []rune(chunks[i-1])
			if len(prev) >= kbOverlapRunes {
				tail := string(prev[len(prev)-kbOverlapRunes:])
				if !strings.Contains(c, tail) {
					t.Fatalf("第 %d 块应包含上一块结尾的 %d 字重叠内容", i, kbOverlapRunes)
				}
			}
		}
	}
}

func TestKBOpenReturnsFullTextAndNeighbors(t *testing.T) {
	kb := NewKB(newMemStore())
	body := "# 长文档" + nl + nl + strings.Repeat("放量突破后的回踩确认是二买点。", 90)
	res := mustImport(t, kb, "", body)
	doc, err := kb.getDocLocked(res.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.ChunkIDs) < 3 {
		t.Fatalf("长文档应切成多块，实际 %d", len(doc.ChunkIDs))
	}
	middle := doc.ChunkIDs[1]
	opened, err := kb.Open(context.Background(), middle)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Content != doc.Content {
		t.Fatal("kb.open 必须返回整篇全文")
	}
	if opened.Chunk == "" {
		t.Fatal("按 chunk 打开时应返回命中块正文")
	}
	if len(opened.Neighbors) != 2 {
		t.Fatalf("中间块应有前后两个邻接块，实际 %d", len(opened.Neighbors))
	}
	if opened.Neighbors[0].Ord != 0 || opened.Neighbors[1].Ord != 2 {
		t.Fatalf("邻接块顺序不对: %+v", opened.Neighbors)
	}
	// 按文档 id 也能打开。
	byDoc, err := kb.Open(context.Background(), doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if byDoc.Content != doc.Content || len(byDoc.Neighbors) == 0 {
		t.Fatalf("按文档 id 打开应返回全文与邻接块: %+v", byDoc)
	}
}

func TestKBListAndStats(t *testing.T) {
	kb := NewKB(newMemStore())
	for i := 0; i < 12; i++ {
		mustImport(t, kb, "文档"+strings.Repeat("x", i+1), "# 标题"+strings.Repeat("y", i+1)+nl+nl+"正文内容")
	}
	if kb.IsEmpty() {
		t.Fatal("导入后不应为空")
	}
	list, err := kb.List(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if list.Total != 12 || len(list.Docs) != 10 {
		t.Fatalf("默认 limit=10、total=12，实际 len=%d total=%d", len(list.Docs), list.Total)
	}
	for _, d := range list.Docs {
		if d.ID == "" || d.Title == "" {
			t.Fatalf("列表项缺少 id/title: %+v", d)
		}
		if len([]rune(d.Summary)) > kbSummaryLimit {
			t.Fatalf("摘要过长: %d", len([]rune(d.Summary)))
		}
	}
	st, err := kb.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Docs != 12 || st.Chunks == 0 || st.Terms == 0 {
		t.Fatalf("统计不对: %+v", st)
	}
}

func TestKBSyncDirIncremental(t *testing.T) {
	dir := t.TempDir()
	kb := NewKB(newMemStore())
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("a.md", "# 情绪周期"+nl+nl+"启动期看分歧，加速期看补涨。")
	write("b.md", "# 仓位管理"+nl+nl+"单票不超过三成，回撤控制优先。")

	first, err := kb.SyncDir(context.Background(), dir, []string{"content"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Imported != 2 || first.Skipped != 0 {
		t.Fatalf("首轮应导入 2 个文件，实际 %+v", first)
	}
	second, err := kb.SyncDir(context.Background(), dir, []string{"content"})
	if err != nil {
		t.Fatal(err)
	}
	if second.Imported != 0 || second.Skipped != 2 {
		t.Fatalf("mtime/size 未变时应全部跳过，实际 %+v", second)
	}

	write("a.md", "# 情绪周期"+nl+nl+"启动期看分歧，加速期看补涨，退潮期空仓等待。")
	third, err := kb.SyncDir(context.Background(), dir, []string{"content"})
	if err != nil {
		t.Fatal(err)
	}
	if third.Imported != 1 || third.Skipped != 1 {
		t.Fatalf("改动 1 个文件应只导入 1 个，实际 %+v", third)
	}

	if err := os.Remove(filepath.Join(dir, "b.md")); err != nil {
		t.Fatal(err)
	}
	fourth, err := kb.SyncDir(context.Background(), dir, []string{"content"})
	if err != nil {
		t.Fatal(err)
	}
	if fourth.Removed != 1 {
		t.Fatalf("删除的文件应从知识库移除，实际 %+v", fourth)
	}
	list, err := kb.List(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if list.Total != 1 {
		t.Fatalf("同步后应剩 1 篇，实际 %d", list.Total)
	}
	// 目录不存在不报错。
	if _, err := kb.SyncDir(context.Background(), filepath.Join(dir, "missing"), nil); err != nil {
		t.Fatalf("目录不存在应跳过而不报错: %v", err)
	}
}

func TestKBImportToolWritesAndIsRetrievable(t *testing.T) {
	kb := NewKB(newMemStore())
	var tool *plugin.Tool
	for _, tl := range KBTools(kb) {
		if tl.Name == "kb.import" {
			tl := tl
			tool = &tl
		}
	}
	if tool == nil {
		t.Fatal("必须提供 kb.import 工具（agent 靠它把复盘结论沉淀进知识库）")
	}
	if tool.Risk != plugin.RiskWrite {
		t.Fatalf("kb.import 风险等级应为 write（只写本地 kb_* bucket），实际 %s", tool.Risk)
	}
	if err := tool.Validate(); err != nil {
		t.Fatal(err)
	}
	payload := `{"title":"11-10 复盘","content":"# 11-10 复盘 结论：情绪回暖，连板高度 4 板，主线是算力。","tags":["run/2026-11-10/postmarket"]}`
	out, err := tool.Run(context.Background(), json.RawMessage(payload))
	if err != nil {
		t.Fatalf("kb.import 失败: %v", err)
	}
	buf, _ := json.Marshal(out)
	if !strings.Contains(string(buf), "chunks") {
		t.Fatalf("kb.import 应返回 id/title/chars/chunks，实际: %s", string(buf))
	}
	res, err := kb.Search(context.Background(), "情绪回暖", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) == 0 || !strings.Contains(res.Hits[0].Snippet, "情绪回暖") {
		t.Fatalf("写入后应能立刻检索到，实际: %+v", res.Hits)
	}
	// 空 content 必须给出可操作中文错误。
	if _, err := tool.Run(context.Background(), json.RawMessage(`{"title":"空"}`)); err == nil ||
		!strings.Contains(err.Error(), "content") {
		t.Fatalf("缺 content 应报错并提示字段，实际: %v", err)
	}
}

func TestKBToolsTolerateEmptyArgs(t *testing.T) {
	kb := seedKB(t)
	tools := KBTools(kb)
	for _, tool := range tools {
		if err := tool.Validate(); err != nil {
			t.Fatalf("%s 不合法: %v", tool.Name, err)
		}
	}
	for _, tool := range tools {
		out, err := tool.Run(context.Background(), nil)
		if tool.Name == "kb.open" || tool.Name == "kb.import" {
			// 这两个工具没有必填参数就无法工作，必须回可操作的中文错误而不是 panic。
			want := "id"
			if tool.Name == "kb.import" {
				want = "content"
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("%s 空参数应返回包含 %q 的中文错误，实际: %v", tool.Name, want, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s 传空参数不应报错（验收会用 {} 直接跑）: %v", tool.Name, err)
		}
		if out == nil {
			t.Fatalf("%s 传空参数应返回结构，不能返回 nil", tool.Name)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
