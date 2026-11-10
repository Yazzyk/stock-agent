// Package knowledge 提供本地知识库与技能目录的插件外壳：
// 方法 list/search/open/import/skills/skill，工具 kb.search/kb.open/kb.list 与 skill.list/skill.view/skill.read。
// 检索与索引实现都在 internal/progressive，本包只做接线与参数校验。
package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yazzyk/stock-agent/internal/progressive"
	"github.com/yazzyk/stock-agent/pkg/plugin"
	"github.com/yazzyk/stock-agent/plugins/all"
)

func init() { all.Register(New) }

// New 构造 knowledge 插件。
func New() plugin.Plugin { return &Knowledge{} }

// Knowledge 是知识库插件。
type Knowledge struct {
	plugin.Base

	kb     *progressive.KB
	skills *progressive.SkillLoader
}

// Manifest 返回插件元信息。
func (k *Knowledge) Manifest() plugin.Manifest {
	return plugin.Manifest{
		ID:          "knowledge",
		Name:        "知识库",
		Version:     "1.0.0",
		Description: "本地知识库与技能目录：中文 bigram 检索、技能按需展开，是渐进式披露的知识来源",
		Tags:        []string{"kb", "skill"},
	}
}

// Activate 注册服务、工具、方法与面板，并尝试自动导入 content/kb。
func (k *Knowledge) Activate(c *plugin.Context) error {
	k.kb = progressive.NewKB(c.Store)
	k.skills = progressive.NewSkillLoader(c.Guard)

	// 服务名固定为 kb.store（API.md 第 2 节）。
	if err := c.Services.Register("kb.store", k.kb); err != nil {
		c.Log.Warn("kb.store 服务注册失败", "error", err)
	}

	for _, t := range progressive.KBTools(k.kb) {
		if err := c.Tools.Add(t); err != nil {
			return fmt.Errorf("注册工具 %s 失败：%w", t.Name, err)
		}
	}
	for _, t := range progressive.SkillTools(k.skills) {
		if err := c.Tools.Add(t); err != nil {
			return fmt.Errorf("注册工具 %s 失败：%w", t.Name, err)
		}
	}

	methods := []plugin.Method{
		{Name: "list", Description: "列出知识库文档", Fn: k.list},
		{Name: "search", Description: "检索知识库", Fn: k.search},
		{Name: "open", Description: "打开文档全文", Fn: k.open},
		{Name: "import", Description: "导入文本或批量导入目录", Fn: k.importDoc},
		{Name: "skills", Description: "列出技能目录", Fn: k.listSkills},
		{Name: "skill", Description: "展开技能正文", Fn: k.viewSkill},
	}
	for _, m := range methods {
		if err := c.Methods.Add(m); err != nil {
			return err
		}
	}

	if err := c.UI.Add(plugin.UISlot{
		ID:        "knowledge",
		Title:     "知识库",
		Icon:      "book",
		Group:     "研究",
		Component: "knowledge",
		Order:     50,
	}); err != nil {
		return err
	}

	k.autoImport(c)
	return nil
}

// autoImport 在知识库为空或 content 变更（mtime/size 变化）时同步 content/kb/*.md。
// 失败只告警，不影响插件启动。
func (k *Knowledge) autoImport(c *plugin.Context) {
	for _, dir := range contentKBDirs() {
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		res, err := k.kb.SyncDir(ctx, dir, []string{"content"})
		cancel()
		if err != nil {
			c.Log.Warn("自动导入知识库失败", "dir", dir, "error", err)
			continue
		}
		c.Log.Info("知识库同步完成", "dir", dir, "imported", res.Imported, "skipped", res.Skipped, "removed", res.Removed, "chunks", res.Chunks)
		if len(res.Warnings) > 0 {
			c.Log.Warn("知识库同步有告警", "warnings", res.Warnings)
		}
	}
}

// contentKBDirs 返回可能要导入的 content/kb 目录：工作目录与可执行文件附近。
func contentKBDirs() []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if p == "" {
			return
		}
		abs, err := filepath.Abs(p)
		if err != nil || seen[abs] {
			return
		}
		seen[abs] = true
		out = append(out, abs)
	}
	if wd, err := os.Getwd(); err == nil {
		add(filepath.Join(wd, "content", "kb"))
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		add(filepath.Join(dir, "content", "kb"))
		add(filepath.Join(filepath.Dir(dir), "content", "kb"))
	}
	return out
}

// ---------- 方法 ----------

func (k *Knowledge) list(ctx context.Context, payload json.RawMessage) (any, error) {
	var in struct {
		Limit int `json:"limit"`
	}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &in); err != nil {
			return nil, fmt.Errorf("list 参数不是合法 JSON：%w", err)
		}
	}
	if in.Limit <= 0 {
		in.Limit = 50
	}
	return k.kb.List(ctx, in.Limit)
}

func (k *Knowledge) search(ctx context.Context, payload json.RawMessage) (any, error) {
	var in struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &in); err != nil {
			return nil, fmt.Errorf("search 参数不是合法 JSON：%w", err)
		}
	}
	return k.kb.Search(ctx, in.Query, in.Limit)
}

func (k *Knowledge) open(ctx context.Context, payload json.RawMessage) (any, error) {
	var in struct {
		ID string `json:"id"`
	}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &in); err != nil {
			return nil, fmt.Errorf("open 参数不是合法 JSON：%w", err)
		}
	}
	return k.kb.Open(ctx, in.ID)
}

func (k *Knowledge) importDoc(ctx context.Context, payload json.RawMessage) (any, error) {
	var in struct {
		Title   string   `json:"title"`
		Content string   `json:"content"`
		Tags    []string `json:"tags"`
		Dir     string   `json:"dir"`
	}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &in); err != nil {
			return nil, fmt.Errorf("import 参数不是合法 JSON：%w", err)
		}
	}
	// 目录模式：批量导入 content/kb 这类平铺目录（增量）。
	if strings.TrimSpace(in.Dir) != "" {
		res, err := k.kb.SyncDir(ctx, in.Dir, in.Tags)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"imported": res.Imported,
			"skipped":  res.Skipped,
			"removed":  res.Removed,
			"files":    res.Files,
			"warnings": res.Warnings,
		}, nil
	}
	if strings.TrimSpace(in.Content) == "" {
		return nil, fmt.Errorf("import 需要 content（正文）或 dir（目录路径）")
	}
	res, err := k.kb.Import(ctx, progressive.ImportInput{
		Title:   in.Title,
		Content: in.Content,
		Tags:    in.Tags,
		Source:  "",
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": res.ID, "title": res.Title, "chars": res.Chars, "chunks": res.Chunks}, nil
}

func (k *Knowledge) listSkills(context.Context, json.RawMessage) (any, error) {
	skills, err := k.skills.List()
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(skills))
	for _, s := range skills {
		items = append(items, map[string]any{
			"name":        s.Name,
			"description": s.Description,
			"tags":        s.Tags,
			"risk":        s.Risk,
			"version":     s.Version,
			"source":      s.Source,
		})
	}
	return map[string]any{"skills": items, "total": len(items)}, nil
}

func (k *Knowledge) viewSkill(_ context.Context, payload json.RawMessage) (any, error) {
	var in struct {
		Name string `json:"name"`
	}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &in); err != nil {
			return nil, fmt.Errorf("skill 参数不是合法 JSON：%w", err)
		}
	}
	s, err := k.skills.View(in.Name)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"name":        s.Name,
		"description": s.Description,
		"tags":        s.Tags,
		"risk":        s.Risk,
		"version":     s.Version,
		"content":     s.Body,
		"resources":   s.Resources,
	}, nil
}
