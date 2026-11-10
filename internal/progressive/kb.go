package progressive

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/yazzyk/stock-agent/pkg/contract"
	"github.com/yazzyk/stock-agent/pkg/plugin"
)

const (
	// 以下 bucket 见 API.md 第 9 节：kb_docs / kb_chunks / kb_meta 归 knowledge 与 progressive 共用。
	kbDocBucket   = "kb_docs"
	kbChunkBucket = "kb_chunks"
	kbMetaBucket  = "kb_meta"
	kbIndexKey    = "index"
	kbManifestKey = "manifest"

	// kbChunkRunes 是分块长度（字符），kbOverlapRunes 是相邻块重叠长度。
	kbChunkRunes   = 600
	kbOverlapRunes = 80
	// kbSnippetLimit 是检索片段的最大字符数。
	kbSnippetLimit = 400
	// kbSummaryLimit 是文档摘要的最大字符数。
	kbSummaryLimit = 120
)

// KB 是基于 contract.Store 的知识库：中文 bigram 倒排索引 + TF-IDF 打分。
// 打分只依赖索引内容与查询，不含时间/随机量，同输入必然同分数同顺序。
type KB struct {
	store contract.Store
	mu    sync.Mutex
}

// NewKB 构造知识库。store 不能为空。
func NewKB(store contract.Store) *KB { return &KB{store: store} }

// Section 是文档里一个 H2 小节。
type Section struct {
	Heading string `json:"heading"`
	Content string `json:"content"`
}

// Doc 是一篇知识库文档。
type Doc struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Source    string    `json:"source,omitempty"`
	Tags      []string  `json:"tags,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
	Chars     int       `json:"chars"`
	ChunkIDs  []string  `json:"chunkIds"`
	Content   string    `json:"content"`
	Sections  []Section `json:"sections,omitempty"`
}

// Chunk 是一个检索单元。
type Chunk struct {
	ID       string `json:"id"`
	DocID    string `json:"docId"`
	DocTitle string `json:"docTitle"`
	Ord      int    `json:"ord"`
	Text     string `json:"text"`
}

// Posting 是倒排表中的一条：chunk id + 词频。
type Posting struct {
	Chunk string `json:"c"`
	TF    int    `json:"t"`
}

// Index 是倒排索引。
type Index struct {
	ChunkCount int                  `json:"chunkCount"`
	Postings   map[string][]Posting `json:"postings"`
	DocLen     map[string]int       `json:"docLen"`
}

// ImportInput 是导入参数。
type ImportInput struct {
	Title   string   `json:"title"`
	Content string   `json:"content"`
	Tags    []string `json:"tags"`
	Source  string   `json:"source"`
}

// ImportResult 是导入结果。
type ImportResult struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Chars    int    `json:"chars"`
	Chunks   int    `json:"chunks"`
	Replaced bool   `json:"replaced"`
}

// Hit 是一条检索命中。
type KBHit struct {
	ID      string  `json:"id"`
	DocID   string  `json:"docId"`
	Title   string  `json:"title"`
	Snippet string  `json:"snippet"`
	Score   float64 `json:"score"`
}

// KBSearchResult 是检索结果。
type KBSearchResult struct {
	Query string  `json:"query"`
	Total int     `json:"total"`
	Hits  []KBHit `json:"hits"`
	Hint  string  `json:"hint,omitempty"`
}

// Neighbor 是相邻块。
type Neighbor struct {
	ID   string `json:"id"`
	Ord  int    `json:"ord"`
	Text string `json:"text"`
}

// OpenResult 是打开结果：全文 + 命中块 + 邻接块。
type OpenResult struct {
	ID        string     `json:"id"`
	DocID     string     `json:"docId"`
	Title     string     `json:"title"`
	Content   string     `json:"content"`
	Tags      []string   `json:"tags,omitempty"`
	Chunk     string     `json:"chunk,omitempty"`
	Neighbors []Neighbor `json:"neighbors,omitempty"`
	Sections  []Section  `json:"sections,omitempty"`
}

// DocSummary 是文档列表项。
type DocSummary struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Summary   string    `json:"summary"`
	Tags      []string  `json:"tags,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
	Chars     int       `json:"chars"`
	Chunks    int       `json:"chunks"`
}

// ListResult 是文档列表。
type ListResult struct {
	Docs  []DocSummary `json:"docs"`
	Total int          `json:"total"`
}

// Stats 是知识库统计。
type Stats struct {
	Docs       int `json:"docs"`
	Chunks     int `json:"chunks"`
	Terms      int `json:"terms"`
	Chars      int `json:"chars"`
	SkippedDoc int `json:"skippedDocs"`
}

// SyncResult 是目录同步结果。
type SyncResult struct {
	Imported int      `json:"imported"`
	Skipped  int      `json:"skipped"`
	Removed  int      `json:"removed"`
	Chunks   int      `json:"chunks"`
	Files    []string `json:"files,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

type manifestEntry struct {
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime"`
	DocID string `json:"docId"`
}

// ---------- 导入 ----------

// Import 写入/覆盖一篇文档并重建倒排索引。
func (k *KB) Import(_ context.Context, in ImportInput) (ImportResult, error) {
	content := strings.TrimSpace(in.Content)
	if content == "" {
		return ImportResult{}, fmt.Errorf("导入内容为空：请提供正文（Markdown）")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	res, err := k.importLocked(in)
	if err != nil {
		return ImportResult{}, err
	}
	if err := k.rebuildLocked(); err != nil {
		return ImportResult{}, err
	}
	return res, nil
}

func (k *KB) importLocked(in ImportInput) (ImportResult, error) {
	content := strings.TrimSpace(in.Content)
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = extractTitle(content, in.Source)
	}
	docID := docIDFor(in.Source, title, content)
	res := ImportResult{ID: docID, Title: title, Chars: utf8.RuneCountInString(content)}

	if prev, err := k.getDocLocked(docID); err == nil {
		if err := k.deleteChunksLocked(prev.ChunkIDs); err != nil {
			return ImportResult{}, err
		}
		res.Replaced = true
	}

	texts := chunkText(content)
	chunks := make([]Chunk, 0, len(texts))
	chunkIDs := make([]string, 0, len(texts))
	for i, t := range texts {
		id := fmt.Sprintf("%s-%03d", docID, i)
		chunks = append(chunks, Chunk{ID: id, DocID: docID, DocTitle: title, Ord: i, Text: t})
		chunkIDs = append(chunkIDs, id)
	}
	doc := Doc{
		ID:        docID,
		Title:     title,
		Source:    in.Source,
		Tags:      in.Tags,
		UpdatedAt: time.Now(),
		Chars:     res.Chars,
		ChunkIDs:  chunkIDs,
		Content:   content,
		Sections:  extractSections(content),
	}
	buf, err := json.Marshal(doc)
	if err != nil {
		return ImportResult{}, fmt.Errorf("文档序列化失败：%w", err)
	}
	if err := k.store.Put(kbDocBucket, docID, buf); err != nil {
		return ImportResult{}, fmt.Errorf("保存文档失败：%w", err)
	}
	for _, c := range chunks {
		cb, err := json.Marshal(c)
		if err != nil {
			return ImportResult{}, fmt.Errorf("分块序列化失败：%w", err)
		}
		if err := k.store.Put(kbChunkBucket, c.ID, cb); err != nil {
			return ImportResult{}, fmt.Errorf("保存分块失败：%w", err)
		}
	}
	res.Chunks = len(chunks)
	return res, nil
}

// ImportFile 读取一个 Markdown 文件并导入（source 记为文件绝对路径）。
func (k *KB) ImportFile(ctx context.Context, path string, tags []string) (ImportResult, error) {
	buf, err := os.ReadFile(path)
	if err != nil {
		return ImportResult{}, fmt.Errorf("读取 %s 失败：%w", path, err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	return k.Import(ctx, ImportInput{Title: "", Content: string(buf), Tags: tags, Source: abs})
}

// SyncDir 增量同步一个目录下的 *.md（平铺，不递归）：文件 mtime/size 未变则跳过，
// 已删除的文件从知识库移除。单文件失败只记 warning，不中断整批。
func (k *KB) SyncDir(ctx context.Context, dir string, tags []string) (SyncResult, error) {
	res := SyncResult{Files: []string{}}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		absDir = dir
	}
	entries, err := os.ReadDir(absDir)
	if err != nil {
		if os.IsNotExist(err) {
			return res, nil // 目录不存在：跳过，不报错
		}
		return res, fmt.Errorf("读取目录 %s 失败：%w", dir, err)
	}
	k.mu.Lock()
	defer k.mu.Unlock()

	manifest := k.loadManifestLocked()
	next := map[string]manifestEntry{}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		path := filepath.Join(absDir, name)
		st, err := os.Stat(path)
		if err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("跳过 %s：%v", name, err))
			continue
		}
		if prev, ok := manifest[path]; ok && prev.Size == st.Size() && prev.Mtime == st.ModTime().UnixNano() {
			next[path] = prev
			res.Skipped++
			continue
		}
		out, err := k.importLocked(ImportInput{Content: readFileString(path), Tags: tags, Source: path})
		if err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("导入 %s 失败：%v", name, err))
			continue
		}
		next[path] = manifestEntry{Size: st.Size(), Mtime: st.ModTime().UnixNano(), DocID: out.ID}
		res.Imported++
		res.Chunks += out.Chunks
		res.Files = append(res.Files, name)
	}

	for path, entry := range manifest {
		if _, ok := next[path]; ok {
			continue
		}
		if doc, err := k.getDocLocked(entry.DocID); err == nil {
			if err := k.deleteChunksLocked(doc.ChunkIDs); err != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("清理旧分块失败：%v", err))
				continue
			}
			_ = k.store.Delete(kbDocBucket, entry.DocID)
			res.Removed++
		}
	}
	if err := k.saveManifestLocked(next); err != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("保存同步清单失败：%v", err))
	}
	if res.Imported > 0 || res.Removed > 0 {
		if err := k.rebuildLocked(); err != nil {
			return res, err
		}
	}
	return res, nil
}

func readFileString(path string) string {
	buf, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(buf)
}

// ---------- 检索 ----------

// Search 检索知识库：TF-IDF 打分，同输入必然同分数同顺序。
func (k *KB) Search(_ context.Context, query string, limit int) (KBSearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		// 空参数（模型传 {}）不报错：返回空列表 + 可操作的中文提示。
		return KBSearchResult{Query: "", Hits: []KBHit{}, Hint: "请提供查询词：例如“涨停 分歧转一致”“北向资金”“复盘 情绪周期”"}, nil
	}
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}
	k.mu.Lock()
	index, err := k.loadIndexLocked()
	k.mu.Unlock()
	if err != nil {
		return KBSearchResult{}, err
	}
	res := KBSearchResult{Query: query, Hits: []KBHit{}}
	if index.ChunkCount == 0 {
		res.Hint = "知识库为空：先用 knowledge 面板的 import 或让 knowledge 插件导入 content/kb/*.md"
		return res, nil
	}
	terms := queryTerms(query)
	scores := map[string]float64{}
	n := float64(index.ChunkCount)
	for _, term := range terms {
		postings := index.Postings[term.Text]
		if len(postings) == 0 {
			continue
		}
		idf := math.Log(1 + n/float64(len(postings)))
		for _, p := range postings {
			tfw := 1 + math.Log(float64(p.TF))
			scores[p.Chunk] += term.Weight * tfw * idf
		}
	}
	type scored struct {
		id    string
		score float64
	}
	list := make([]scored, 0, len(scores))
	for id, s := range scores {
		list = append(list, scored{id: id, score: round6(s)})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].score != list[j].score {
			return list[i].score > list[j].score
		}
		return list[i].id < list[j].id
	})
	res.Total = len(list)
	if len(list) > limit {
		list = list[:limit]
	}
	for _, s := range list {
		chunk, err := k.getChunkLocked(s.id)
		if err != nil {
			continue
		}
		res.Hits = append(res.Hits, KBHit{
			ID:      chunk.ID,
			DocID:   chunk.DocID,
			Title:   chunk.DocTitle,
			Snippet: snippet(chunk.Text, terms),
			Score:   s.score,
		})
	}
	if res.Total == 0 {
		res.Hint = "没有命中：换更短的关键词（如“涨停”“资金”“复盘”），或用 kb.list 看有哪些文档"
	}
	return res, nil
}

// Open 打开一篇文档：返回全文，并给出命中块与邻接块。id 可以是 chunk id 或 doc id。
func (k *KB) Open(_ context.Context, id string) (OpenResult, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return OpenResult{}, fmt.Errorf("kb.open 需要 id（来自 kb.search 的 id 或 kb.list 的 id）")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	var chunk *Chunk
	if c, err := k.getChunkLocked(id); err == nil {
		chunk = &c
	}
	docID := id
	if chunk != nil {
		docID = chunk.DocID
	}
	doc, err := k.getDocLocked(docID)
	if err != nil {
		return OpenResult{}, fmt.Errorf("文档不存在：%s（用 kb.search 或 kb.list 找到正确 id）", id)
	}
	out := OpenResult{
		ID:       id,
		DocID:    doc.ID,
		Title:    doc.Title,
		Content:  doc.Content,
		Tags:     doc.Tags,
		Sections: doc.Sections,
	}
	if chunk != nil {
		out.Chunk = chunk.Text
		out.Neighbors = k.neighborsLocked(doc, chunk.Ord)
	} else {
		// doc id 直接打开：把前 3 块作为邻接块，方便模型继续深入。
		max := 3
		if len(doc.ChunkIDs) < max {
			max = len(doc.ChunkIDs)
		}
		out.Neighbors = k.neighborsLocked(doc, max)
	}
	return out, nil
}

func (k *KB) neighborsLocked(doc Doc, ord int) []Neighbor {
	out := []Neighbor{}
	for _, target := range []int{ord - 1, ord + 1} {
		if target < 0 || target >= len(doc.ChunkIDs) {
			continue
		}
		c, err := k.getChunkLocked(doc.ChunkIDs[target])
		if err != nil {
			continue
		}
		out = append(out, Neighbor{ID: c.ID, Ord: c.Ord, Text: TruncateRunes(c.Text, kbSnippetLimit)})
	}
	return out
}

// List 按更新时间倒序返回文档列表。
func (k *KB) List(_ context.Context, limit int) (ListResult, error) {
	if limit <= 0 {
		limit = 10
	}
	if limit > 200 {
		limit = 200
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	docs, err := k.allDocsLocked()
	if err != nil {
		return ListResult{}, err
	}
	sort.Slice(docs, func(i, j int) bool {
		if !docs[i].UpdatedAt.Equal(docs[j].UpdatedAt) {
			return docs[i].UpdatedAt.After(docs[j].UpdatedAt)
		}
		return docs[i].ID < docs[j].ID
	})
	res := ListResult{Docs: make([]DocSummary, 0, limit), Total: len(docs)}
	for _, d := range docs {
		if len(res.Docs) >= limit {
			break
		}
		res.Docs = append(res.Docs, DocSummary{
			ID:        d.ID,
			Title:     d.Title,
			Summary:   docSummary(d.Content),
			Tags:      d.Tags,
			UpdatedAt: d.UpdatedAt,
			Chars:     d.Chars,
			Chunks:    len(d.ChunkIDs),
		})
	}
	return res, nil
}

// Stats 返回知识库统计。
func (k *KB) Stats(_ context.Context) (Stats, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	docs, err := k.allDocsLocked()
	if err != nil {
		return Stats{}, err
	}
	index, err := k.loadIndexLocked()
	if err != nil {
		return Stats{}, err
	}
	st := Stats{Docs: len(docs), Chunks: index.ChunkCount, Terms: len(index.Postings)}
	for _, d := range docs {
		st.Chars += d.Chars
	}
	return st, nil
}

// IsEmpty 判断知识库是否没有任何文档。
func (k *KB) IsEmpty() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	docs, err := k.allDocsLocked()
	return err != nil || len(docs) == 0
}

// ---------- 内部：存储访问 ----------

func (k *KB) getDocLocked(id string) (Doc, error) {
	buf, err := k.store.Get(kbDocBucket, id)
	if err != nil {
		return Doc{}, err
	}
	var d Doc
	if err := json.Unmarshal(buf, &d); err != nil {
		return Doc{}, fmt.Errorf("文档 %s 损坏：%w", id, err)
	}
	return d, nil
}

func (k *KB) getChunkLocked(id string) (Chunk, error) {
	buf, err := k.store.Get(kbChunkBucket, id)
	if err != nil {
		return Chunk{}, err
	}
	var c Chunk
	if err := json.Unmarshal(buf, &c); err != nil {
		return Chunk{}, fmt.Errorf("分块 %s 损坏：%w", id, err)
	}
	return c, nil
}

func (k *KB) allDocsLocked() ([]Doc, error) {
	items, err := k.store.List(kbDocBucket, "")
	if err != nil {
		return nil, fmt.Errorf("读取文档列表失败：%w", err)
	}
	out := make([]Doc, 0, len(items))
	for _, it := range items {
		var d Doc
		if err := json.Unmarshal(it.Value, &d); err != nil {
			continue
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (k *KB) deleteChunksLocked(ids []string) error {
	for _, id := range ids {
		if err := k.store.Delete(kbChunkBucket, id); err != nil && err != contract.ErrNotFound {
			return fmt.Errorf("删除旧分块失败：%w", err)
		}
	}
	return nil
}

func (k *KB) loadIndexLocked() (Index, error) {
	buf, err := k.store.Get(kbMetaBucket, kbIndexKey)
	if err != nil {
		return Index{Postings: map[string][]Posting{}, DocLen: map[string]int{}}, nil
	}
	var idx Index
	if err := json.Unmarshal(buf, &idx); err != nil {
		return Index{Postings: map[string][]Posting{}, DocLen: map[string]int{}}, nil
	}
	if idx.Postings == nil {
		idx.Postings = map[string][]Posting{}
	}
	if idx.DocLen == nil {
		idx.DocLen = map[string]int{}
	}
	return idx, nil
}

// rebuildLocked 从全部分块重建倒排索引（分块按 id 排序，保证索引与打分可复现）。
func (k *KB) rebuildLocked() error {
	items, err := k.store.List(kbChunkBucket, "")
	if err != nil {
		return fmt.Errorf("重建索引失败：%w", err)
	}
	chunks := make([]Chunk, 0, len(items))
	for _, it := range items {
		var c Chunk
		if err := json.Unmarshal(it.Value, &c); err != nil {
			continue
		}
		chunks = append(chunks, c)
	}
	sort.Slice(chunks, func(i, j int) bool { return chunks[i].ID < chunks[j].ID })

	idx := Index{ChunkCount: len(chunks), Postings: map[string][]Posting{}, DocLen: map[string]int{}}
	for _, c := range chunks {
		tokens := indexTokens(c.Text)
		idx.DocLen[c.ID] = len(tokens)
		counts := map[string]int{}
		for _, t := range tokens {
			counts[t]++
		}
		terms := make([]string, 0, len(counts))
		for t := range counts {
			terms = append(terms, t)
		}
		sort.Strings(terms)
		for _, t := range terms {
			idx.Postings[t] = append(idx.Postings[t], Posting{Chunk: c.ID, TF: counts[t]})
		}
	}
	buf, err := json.Marshal(idx)
	if err != nil {
		return fmt.Errorf("索引序列化失败：%w", err)
	}
	if err := k.store.Put(kbMetaBucket, kbIndexKey, buf); err != nil {
		return fmt.Errorf("保存索引失败：%w", err)
	}
	return nil
}

func (k *KB) loadManifestLocked() map[string]manifestEntry {
	out := map[string]manifestEntry{}
	buf, err := k.store.Get(kbMetaBucket, kbManifestKey)
	if err != nil {
		return out
	}
	_ = json.Unmarshal(buf, &out)
	if out == nil {
		out = map[string]manifestEntry{}
	}
	return out
}

func (k *KB) saveManifestLocked(m map[string]manifestEntry) error {
	buf, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return k.store.Put(kbMetaBucket, kbManifestKey, buf)
}

// ---------- 内部：文本处理 ----------

// docIDFor 生成稳定的文档 id：有来源时按来源，否则按标题+正文摘要。
func docIDFor(source, title, content string) string {
	h := sha1.New()
	if source != "" {
		_, _ = h.Write([]byte("source:" + source))
	} else {
		_, _ = h.Write([]byte("title:" + title))
		_, _ = h.Write([]byte("|content:"))
		sum := sha1.Sum([]byte(content))
		_, _ = h.Write(sum[:])
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// extractTitle 取第一个 H1 作为标题；没有则用来源文件名；再没有就用"未命名文档"。
func extractTitle(content, source string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			if t := strings.TrimSpace(strings.TrimPrefix(line, "# ")); t != "" {
				return t
			}
		}
	}
	if source != "" {
		base := strings.TrimSuffix(filepath.Base(source), filepath.Ext(source))
		if base != "" {
			return base
		}
	}
	return "未命名文档"
}

// extractSections 按 H2 切小节。
func extractSections(content string) []Section {
	lines := strings.Split(content, "\n")
	sections := []Section{}
	var cur *Section
	var body []string
	flush := func() {
		if cur == nil {
			return
		}
		cur.Content = strings.TrimSpace(strings.Join(body, "\n"))
		sections = append(sections, *cur)
		body = body[:0]
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "## ") {
			flush()
			cur = &Section{Heading: strings.TrimSpace(strings.TrimPrefix(line, "## "))}
			continue
		}
		if cur != nil {
			body = append(body, line)
		}
	}
	flush()
	return sections
}

// chunkText 按 ~600 字符分块、重叠 80 字符，优先在段落边界切分，结果可复现。
func chunkText(text string) []string {
	r := []rune(text)
	if len(r) == 0 {
		return nil
	}
	if len(r) <= kbChunkRunes {
		if s := strings.TrimSpace(string(r)); s != "" {
			return []string{s}
		}
		return nil
	}
	out := []string{}
	start := 0
	for start < len(r) {
		end := start + kbChunkRunes
		if end > len(r) {
			end = len(r)
		}
		if end < len(r) {
			if cut := findBoundary(r, end, start); cut > start {
				end = cut
			}
		}
		seg := strings.TrimSpace(string(r[start:end]))
		if seg != "" {
			out = append(out, seg)
		}
		if end >= len(r) {
			break
		}
		next := end - kbOverlapRunes
		if next <= start {
			next = start + 1
		}
		start = next
	}
	return out
}

func findBoundary(r []rune, end, start int) int {
	lo := end - 120
	if lo < start+1 {
		lo = start + 1
	}
	for i := end; i > lo; i-- {
		if i-1 >= 0 && r[i-1] == '\n' && i < len(r) && r[i] == '\n' {
			return i
		}
	}
	hi := end + 60
	if hi > len(r) {
		hi = len(r)
	}
	for i := end; i < hi; i++ {
		if r[i] == '\n' {
			return i
		}
	}
	return end
}

// indexTokens 索引侧分词：中文单字 + 相邻双字（bigram）+ ASCII 词（≥2，小写）。
func indexTokens(s string) []string {
	out := make([]string, 0, utf8.RuneCountInString(s))
	var han []rune
	var word []rune
	flushHan := func() {
		if len(han) == 0 {
			return
		}
		for i := 0; i < len(han); i++ {
			out = append(out, string(han[i]))
		}
		for i := 0; i+1 < len(han); i++ {
			out = append(out, string(han[i:i+2]))
		}
		han = han[:0]
	}
	flushWord := func() {
		if len(word) >= 2 {
			out = append(out, strings.ToLower(string(word)))
		}
		word = word[:0]
	}
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Han, r):
			flushWord()
			han = append(han, r)
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			flushHan()
			word = append(word, r)
		default:
			flushHan()
			flushWord()
		}
	}
	flushHan()
	flushWord()
	return out
}

type queryTerm struct {
	Text   string
	Weight float64
}

// queryTerms 查询侧分词：中文单字（权重 0.6）/ 双字（1.0）+ ASCII 词（1.0），去重取最大权重。
func queryTerms(s string) []queryTerm {
	weights := map[string]float64{}
	var han []rune
	var word []rune
	flushHan := func() {
		if len(han) == 0 {
			return
		}
		if len(han) == 1 {
			weights[string(han[0])] = math.Max(weights[string(han[0])], 0.6)
		} else {
			for i := 0; i+1 < len(han); i++ {
				t := string(han[i : i+2])
				weights[t] = math.Max(weights[t], 1.0)
			}
		}
		han = han[:0]
	}
	flushWord := func() {
		if len(word) >= 2 {
			w := strings.ToLower(string(word))
			weights[w] = math.Max(weights[w], 1.0)
		}
		word = word[:0]
	}
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Han, r):
			flushWord()
			han = append(han, r)
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			flushHan()
			word = append(word, r)
		default:
			flushHan()
			flushWord()
		}
	}
	flushHan()
	flushWord()
	terms := make([]queryTerm, 0, len(weights))
	for t, w := range weights {
		terms = append(terms, queryTerm{Text: t, Weight: w})
	}
	sort.Slice(terms, func(i, j int) bool { return terms[i].Text < terms[j].Text })
	return terms
}

// snippet 生成命中片段：以首个命中词为中心取窗口，最多 kbSnippetLimit 字。
func snippet(text string, terms []queryTerm) string {
	r := []rune(text)
	if len(r) <= kbSnippetLimit {
		return text
	}
	pos := -1
	for _, t := range terms {
		if idx := strings.Index(text, t.Text); idx >= 0 {
			if pos < 0 || idx < pos {
				pos = idx
			}
		}
	}
	if pos < 0 {
		return TruncateRunes(text, kbSnippetLimit)
	}
	// 把字节位置换算成 rune 位置。
	startRune := utf8.RuneCountInString(text[:pos])
	half := kbSnippetLimit / 2
	from := startRune - half
	if from < 0 {
		from = 0
	}
	to := from + kbSnippetLimit
	if to > len(r) {
		to = len(r)
		from = to - kbSnippetLimit
		if from < 0 {
			from = 0
		}
	}
	out := string(r[from:to])
	if from > 0 {
		out = "…" + out
	}
	if to < len(r) {
		out += "…"
	}
	return out
}

func docSummary(content string) string {
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		l := strings.TrimSpace(strings.TrimLeft(line, "#>-* "))
		if l == "" {
			continue
		}
		return TruncateRunes(l, kbSummaryLimit)
	}
	return ""
}

func round6(v float64) float64 { return math.Round(v*1e6) / 1e6 }

// ---------- 工具 ----------

// KBTools 返回 kb.search / kb.open / kb.list 三个工具（Risk=read）。
func KBTools(k *KB) []plugin.Tool {
	return []plugin.Tool{
		{
			Name:     "kb.search",
			Title:    "检索知识库",
			Summary:  "按关键词搜本地知识库，返回片段与分数；先搜再打开",
			Tags:     []string{"kb"},
			Risk:     plugin.RiskRead,
			Schema:   json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"中文关键词，如 涨停 分歧转一致 / 北向资金"},"limit":{"type":"integer","description":"返回条数，默认 8，上限 50"}},"required":["query"]}`),
			Examples: []string{`{"query":"涨停 分歧","limit":5}`},
			Run: func(ctx context.Context, args json.RawMessage) (any, error) {
				var in struct {
					Query string `json:"query"`
					Limit int    `json:"limit"`
				}
				if len(args) > 0 {
					if err := json.Unmarshal(args, &in); err != nil {
						return nil, fmt.Errorf("kb.search 参数不是合法 JSON：%w", err)
					}
				}
				return k.Search(ctx, in.Query, in.Limit)
			},
		},
		{
			Name:     "kb.open",
			Title:    "打开知识库文档",
			Summary:  "按 id 打开全文与邻接块（id 来自 kb.search/kb.list）",
			Tags:     []string{"kb"},
			Risk:     plugin.RiskRead,
			Schema:   json.RawMessage(`{"type":"object","properties":{"id":{"type":"string","description":"kb.search 命中 id 或 kb.list 的文档 id"}},"required":["id"]}`),
			Examples: []string{`{"id":"a1b2c3d4e5f60789-000"}`},
			Run: func(ctx context.Context, args json.RawMessage) (any, error) {
				var in struct {
					ID string `json:"id"`
				}
				if len(args) > 0 {
					if err := json.Unmarshal(args, &in); err != nil {
						return nil, fmt.Errorf("kb.open 参数不是合法 JSON：%w", err)
					}
				}
				return k.Open(ctx, in.ID)
			},
		},
		{
			Name:     "kb.import",
			Title:    "写入知识库",
			Summary:  "把一条研究结论/复盘笔记写入本地知识库，供以后检索",
			Tags:     []string{"kb"},
			Risk:     plugin.RiskWrite,
			Schema:   json.RawMessage(`{"type":"object","properties":{"title":{"type":"string","description":"标题，缺省取正文第一个 H1"},"content":{"type":"string","description":"Markdown 正文，建议含结论与依据"},"tags":{"type":"array","items":{"type":"string"},"description":"标签，如 run/2026-11-10/postmarket"}},"required":["content"]}`),
			Examples: []string{`{"title":"11-10 复盘","content":"# 11-10 复盘 ｜ ## 结论 ｜ 情绪回暖，连板高度 4 板","tags":["run/2026-11-10/postmarket"]}`},
			Run: func(ctx context.Context, args json.RawMessage) (any, error) {
				var in struct {
					Title   string   `json:"title"`
					Content string   `json:"content"`
					Tags    []string `json:"tags"`
				}
				if len(args) > 0 {
					if err := json.Unmarshal(args, &in); err != nil {
						return nil, fmt.Errorf("kb.import 参数不是合法 JSON：%w", err)
					}
				}
				if strings.TrimSpace(in.Content) == "" {
					return nil, fmt.Errorf("kb.import 需要 content（Markdown 正文）")
				}
				res, err := k.Import(ctx, ImportInput{Title: in.Title, Content: in.Content, Tags: in.Tags})
				if err != nil {
					return nil, err
				}
				return map[string]any{"id": res.ID, "title": res.Title, "chars": res.Chars, "chunks": res.Chunks}, nil
			},
		},
		{
			Name:     "kb.list",
			Title:    "知识库目录",
			Summary:  "列出知识库文档（标题+摘要+标签），不确定搜什么时先看",
			Tags:     []string{"kb"},
			Risk:     plugin.RiskRead,
			Schema:   json.RawMessage(`{"type":"object","properties":{"limit":{"type":"integer","description":"返回条数，默认 50，上限 200"}}}`),
			Examples: []string{`{"limit":20}`},
			Run: func(ctx context.Context, args json.RawMessage) (any, error) {
				var in struct {
					Limit int `json:"limit"`
				}
				if len(args) > 0 {
					if err := json.Unmarshal(args, &in); err != nil {
						return nil, fmt.Errorf("kb.list 参数不是合法 JSON：%w", err)
					}
				}
				return k.List(ctx, in.Limit)
			},
		},
	}
}
