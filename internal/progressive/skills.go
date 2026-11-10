package progressive

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/yazzyk/stock-agent/pkg/contract"
	"github.com/yazzyk/stock-agent/pkg/plugin"
)

const (
	// SkillFileName 是技能定义文件的固定文件名。
	SkillFileName = "SKILL.md"
	// SkillDescMaxRunes 是 L1 目录里描述的最大字数（超出截断并告警，不报错）。
	SkillDescMaxRunes = 60
	// skillReadMaxBytes 是 skill.read 单次读取上限。
	skillReadMaxBytes = 256 * 1024
)

// Skill 是一个已加载的技能。
type Skill struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Risk        string   `json:"risk"`
	Version     string   `json:"version"`
	Dir         string   `json:"-"`
	Body        string   `json:"-"`
	Resources   []string `json:"resources,omitempty"`
	Source      string   `json:"source"` // content | user
}

type skillFrontmatter struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Tags        []string `yaml:"tags"`
	Risk        string   `yaml:"risk"`
	Version     string   `yaml:"version"`
}

// SkillLoader 从多个根目录加载技能目录。
// 同名技能以"后加载的根"为准（用户目录排在 content 之后，因此用户目录优先）。
// 每次访问按目录 mtime 刷新，不做常驻缓存失效猜测。
type SkillLoader struct {
	guard contract.Guard
	roots []string
	log   *slog.Logger

	mu     sync.Mutex
	cache  []Skill
	sig    string
	warned map[string]bool
}

// NewSkillLoader 构造加载器；roots 为空时使用 DefaultSkillRoots()。
func NewSkillLoader(guard contract.Guard, roots ...string) *SkillLoader {
	if len(roots) == 0 {
		roots = DefaultSkillRoots()
	}
	abs := make([]string, 0, len(roots))
	seen := map[string]bool{}
	for _, r := range roots {
		if strings.TrimSpace(r) == "" {
			continue
		}
		a, err := filepath.Abs(r)
		if err != nil || seen[a] {
			continue
		}
		seen[a] = true
		abs = append(abs, a)
	}
	return &SkillLoader{guard: guard, roots: abs, log: slog.Default(), warned: map[string]bool{}}
}

// DefaultSkillRoots 返回默认技能根目录：仓库 content/skills（工作目录与可执行文件附近各试一次）+ ~/.guanlan/skills。
// 用户目录放在最后，保证同名技能以用户目录为准。
func DefaultSkillRoots() []string {
	var roots []string
	if wd, err := os.Getwd(); err == nil {
		roots = append(roots, filepath.Join(wd, "content", "skills"))
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		roots = append(roots,
			filepath.Join(dir, "content", "skills"),
			filepath.Join(filepath.Dir(dir), "content", "skills"),
		)
	}
	home, _ := os.UserHomeDir()
	if home != "" {
		roots = append(roots, filepath.Join(home, ".guanlan", "skills"))
	}
	return roots
}

// Roots 返回实际使用的技能根目录。
func (l *SkillLoader) Roots() []string { return append([]string{}, l.roots...) }

// List 返回技能目录（L1），按名称排序。
func (l *SkillLoader) List() ([]Skill, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	sig := l.signatureLocked()
	if sig == l.sig && l.cache != nil {
		return append([]Skill{}, l.cache...), nil
	}
	loaded := l.reloadLocked()
	l.cache = loaded
	l.sig = sig
	return append([]Skill{}, loaded...), nil
}

// Refresh 强制重新扫描目录。
func (l *SkillLoader) Refresh() ([]Skill, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cache = l.reloadLocked()
	l.sig = l.signatureLocked()
	return append([]Skill{}, l.cache...), nil
}

// View 返回单个技能（含正文）。
func (l *SkillLoader) View(name string) (Skill, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Skill{}, fmt.Errorf("skill.view 需要技能名（先用 skill.list 查看目录）")
	}
	skills, err := l.List()
	if err != nil {
		return Skill{}, err
	}
	for _, s := range skills {
		if s.Name == name {
			return s, nil
		}
	}
	names := make([]string, 0, len(skills))
	for _, s := range skills {
		names = append(names, s.Name)
	}
	return Skill{}, fmt.Errorf("技能不存在：%s（可用技能：%s）", name, strings.Join(names, ", "))
}

// Read 读取技能目录内的引用文件。路径必须留在该技能目录内，越界（含 ../ 与软链）一律拒绝。
func (l *SkillLoader) Read(name, rel string) ([]byte, error) {
	s, err := l.View(name)
	if err != nil {
		return nil, err
	}
	raw := strings.TrimSpace(rel)
	if raw == "" {
		return nil, fmt.Errorf("skill.read 需要相对路径，例如 references/checklist.md")
	}
	if filepath.IsAbs(raw) {
		return nil, fmt.Errorf("skill.read 只接受技能目录内的相对路径，拒绝绝对路径：%s", raw)
	}
	clean := filepath.Clean(raw)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("skill.read 拒绝路径穿越：%s", raw)
	}
	dirReal := evalSymlinks(s.Dir)
	target := filepath.Join(s.Dir, clean)
	info, err := os.Lstat(target)
	if err != nil {
		return nil, fmt.Errorf("技能 %s 不存在文件 %s", s.Name, raw)
	}
	// 软链一律解到真实路径再判断是否仍在技能目录内（防止 leak.md → ../secret.md）。
	targetReal := evalSymlinks(target)
	if targetReal == filepath.Clean(target) {
		// 解不开（例如悬空软链）时退回"父目录真实路径 + 文件名"。
		targetReal = filepath.Join(evalSymlinks(filepath.Dir(target)), filepath.Base(target))
	}
	if !withinDir(targetReal, dirReal) {
		return nil, fmt.Errorf("skill.read 拒绝读取技能目录之外的文件：%s", raw)
	}
	// 技能目录本身在安全根内时，目标文件也必须通过 Guard.CheckPath。
	if l.guard != nil && l.guard.CheckPath(s.Dir) == nil {
		if err := l.guard.CheckPath(target); err != nil {
			return nil, fmt.Errorf("skill.read 被安全策略拒绝：%w", err)
		}
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("技能 %s 的 %s 不是普通文件", s.Name, raw)
	}
	if info.Size() > skillReadMaxBytes {
		return nil, fmt.Errorf("技能 %s 的 %s 超过 %d 字节，拒绝读取", s.Name, raw, skillReadMaxBytes)
	}
	buf, err := os.ReadFile(target)
	if err != nil {
		return nil, fmt.Errorf("读取技能文件失败：%w", err)
	}
	return buf, nil
}

// signatureLocked 用各 SKILL.md 的 path/size/mtime 组成缓存签名。
func (l *SkillLoader) signatureLocked() string {
	var b strings.Builder
	for _, root := range l.roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			f := filepath.Join(root, e.Name(), SkillFileName)
			st, err := os.Stat(f)
			if err != nil {
				continue
			}
			fmt.Fprintf(&b, "%s|%d|%d;", f, st.Size(), st.ModTime().UnixNano())
		}
	}
	return b.String()
}

func (l *SkillLoader) reloadLocked() []Skill {
	byName := map[string]Skill{}
	order := make([]string, 0, 16)
	for _, root := range l.roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue // 目录不存在就跳过，不报错
		}
		source := "content"
		if strings.Contains(root, filepath.Join(".guanlan", "skills")) {
			source = "user"
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			dir := filepath.Join(root, e.Name())
			skill, err := loadSkillDir(dir, source)
			if err != nil {
				l.warnOnce("skip:"+dir, "技能加载跳过", "dir", dir, "error", err)
				continue
			}
			if _, ok := byName[skill.Name]; !ok {
				order = append(order, skill.Name)
			}
			byName[skill.Name] = skill // 后加载的根覆盖，实现"用户目录优先"
		}
	}
	out := make([]Skill, 0, len(order))
	for _, n := range order {
		out = append(out, byName[n])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (l *SkillLoader) warnOnce(key, msg string, args ...any) {
	if l.warned[key] {
		return
	}
	l.warned[key] = true
	l.log.Warn(msg, args...)
}

func loadSkillDir(dir, source string) (Skill, error) {
	path := filepath.Join(dir, SkillFileName)
	buf, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, fmt.Errorf("缺少 %s", SkillFileName)
	}
	meta, body, err := splitFrontmatter(string(buf))
	if err != nil {
		return Skill{}, err
	}
	name := strings.TrimSpace(meta.Name)
	if name == "" {
		name = filepath.Base(dir)
	}
	desc := strings.TrimSpace(meta.Description)
	if desc == "" {
		desc = name
		slog.Default().Warn("技能缺少 description，已用名称代替", "skill", name, "dir", dir)
	} else if utf8.RuneCountInString(desc) > SkillDescMaxRunes {
		slog.Default().Warn("技能 description 超过 60 字，L1 目录将截断", "skill", name, "dir", dir)
	}
	risk := strings.TrimSpace(meta.Risk)
	switch risk {
	case "read", "network", "write":
	default:
		risk = "read"
	}
	resources := listResources(dir)
	return Skill{
		Name:        name,
		Description: desc,
		Tags:        meta.Tags,
		Risk:        risk,
		Version:     meta.Version,
		Dir:         dir,
		Body:        strings.TrimSpace(body),
		Resources:   resources,
		Source:      source,
	}, nil
}

// splitFrontmatter 解析 "---" 包裹的 YAML frontmatter，返回元信息与正文。
func splitFrontmatter(content string) (skillFrontmatter, string, error) {
	var meta skillFrontmatter
	content = strings.ReplaceAll(content, "\r\n", "\n")
	if !strings.HasPrefix(content, "---") {
		return meta, content, nil
	}
	rest := strings.TrimPrefix(content, "---")
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return meta, content, nil
	}
	head := rest[:idx]
	body := rest[idx+len("\n---"):]
	meta = parseFrontmatter(head)
	return meta, body, nil
}

// parseFrontmatter 解析技能 frontmatter 的 YAML 子集：
// 标量键值、flow 列表（tags: [a, b]）、块列表（tags 换行 + "- a"）。
// 未知字段一律忽略（前向兼容），解析失败不报错而是按缺省处理。
func parseFrontmatter(head string) skillFrontmatter {
	var meta skillFrontmatter
	lines := strings.Split(strings.ReplaceAll(head, "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		colon := strings.Index(line, ":")
		if colon <= 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(line[:colon]))
		val := strings.TrimSpace(line[colon+1:])
		switch key {
		case "name":
			meta.Name = unquote(val)
		case "description":
			meta.Description = unquote(val)
		case "risk":
			meta.Risk = unquote(val)
		case "version":
			meta.Version = unquote(val)
		case "tags":
			if val != "" {
				meta.Tags = parseFlowList(val)
				break
			}
			for i+1 < len(lines) {
				next := strings.TrimSpace(lines[i+1])
				if !strings.HasPrefix(next, "- ") && next != "-" {
					break
				}
				if item := unquote(strings.TrimSpace(strings.TrimPrefix(next, "- "))); item != "" {
					meta.Tags = append(meta.Tags, item)
				}
				i++
			}
		}
	}
	return meta
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

func parseFlowList(v string) []string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "[")
	v = strings.TrimSuffix(v, "]")
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if item := unquote(strings.TrimSpace(p)); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func listResources(dir string) []string {
	out := []string{}
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil || rel == SkillFileName {
			return nil
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	sort.Strings(out)
	return out
}

func evalSymlinks(p string) string {
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.Clean(real)
	}
	return filepath.Clean(p)
}

func withinDir(path, dir string) bool {
	path, dir = filepath.Clean(path), filepath.Clean(dir)
	if path == dir {
		return true
	}
	return strings.HasPrefix(path, dir+string(filepath.Separator))
}

// SkillCatalog 是技能 L1 目录：只输出名称 + 描述 + 标签 + 风险，正文用 skill.view 展开。
type SkillCatalog struct {
	Loader       *SkillLoader
	BudgetTokens int
}

// NewSkillCatalog 构造技能目录生成器。
func NewSkillCatalog(loader *SkillLoader, budgetTokens int) *SkillCatalog {
	if budgetTokens <= 0 {
		budgetTokens = 400
	}
	return &SkillCatalog{Loader: loader, BudgetTokens: budgetTokens}
}

// Index 生成技能目录文本，超预算时按风险与名称顺序裁剪。
func (c *SkillCatalog) Index() IndexResult {
	skills, _ := c.Loader.List()
	res := IndexResult{BudgetTokens: c.BudgetTokens, Included: []IndexEntry{}, Dropped: []IndexEntry{}}
	if len(skills) == 0 {
		res.Text = "（暂无技能：content/skills 与 ~/.guanlan/skills 下都没有 SKILL.md）"
		res.EstimatedTokens = EstimateTokens(res.Text)
		return res
	}
	entries := make([]IndexEntry, 0, len(skills))
	for _, s := range skills {
		entries = append(entries, IndexEntry{
			Name:    s.Name,
			Summary: TruncateRunes(s.Description, SkillDescMaxRunes),
			Tags:    append([]string{}, s.Tags...),
			Risk:    s.Risk,
			Domain:  s.Risk,
		})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		ri, rj := riskRank(plugin.RiskLevel(entries[i].Risk)), riskRank(plugin.RiskLevel(entries[j].Risk))
		if ri != rj {
			return ri < rj
		}
		return entries[i].Name < entries[j].Name
	})
	included := append([]IndexEntry{}, entries...)
	var text string
	for {
		text = renderSkills(included)
		res.EstimatedTokens = EstimateTokens(text)
		if res.EstimatedTokens <= c.BudgetTokens || len(included) == 0 {
			break
		}
		included = included[:len(included)-1]
	}
	kept := map[string]bool{}
	for _, e := range included {
		kept[e.Name] = true
	}
	for _, e := range entries {
		if !kept[e.Name] {
			res.Dropped = append(res.Dropped, e)
		}
	}
	res.Included = included
	res.Text = text
	res.Truncated = len(res.Dropped) > 0
	if res.Truncated {
		res.Hint = fmt.Sprintf("> 还有 %d 个技能未列出：用 skill.list 看全量，用 skill.view 展开正文。", len(res.Dropped))
	}
	return res
}

func renderSkills(included []IndexEntry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## 技能目录（L1，共 %d 个；正文用 skill.view 展开，引用文件用 skill.read）\n", len(included))
	for _, e := range included {
		fmt.Fprintf(&b, "- %s [%s] %s\n", e.Name, e.Risk, e.Summary)
	}
	return b.String()
}

// SkillTools 返回 skill.list / skill.view / skill.read 三个工具（Risk=read）。
func SkillTools(l *SkillLoader) []plugin.Tool {
	return []plugin.Tool{
		{
			Name:     "skill.list",
			Title:    "技能目录",
			Summary:  "列出可用技能（名称+一句话用途），做计划前先看这里",
			Tags:     []string{"skill"},
			Risk:     plugin.RiskRead,
			Schema:   json.RawMessage(`{"type":"object","properties":{}}`),
			Examples: []string{`{}`},
			Run: func(ctx context.Context, args json.RawMessage) (any, error) {
				skills, err := l.List()
				if err != nil {
					return nil, err
				}
				items := make([]map[string]any, 0, len(skills))
				for _, s := range skills {
					items = append(items, map[string]any{
						"name":        s.Name,
						"description": TruncateRunes(s.Description, SkillDescMaxRunes),
						"tags":        s.Tags,
						"risk":        s.Risk,
						"version":     s.Version,
						"source":      s.Source,
						"resources":   s.Resources,
					})
				}
				return map[string]any{"skills": items, "total": len(items)}, nil
			},
		},
		{
			Name:     "skill.view",
			Title:    "展开技能正文",
			Summary:  "读某个技能的完整步骤与模板（skill.list 之后调用）",
			Tags:     []string{"skill"},
			Risk:     plugin.RiskRead,
			Schema:   json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"技能名，来自 skill.list"}},"required":["name"]}`),
			Examples: []string{`{"name":"premarket-plan"}`},
			Run: func(ctx context.Context, args json.RawMessage) (any, error) {
				var in struct {
					Name string `json:"name"`
				}
				if len(args) > 0 {
					if err := json.Unmarshal(args, &in); err != nil {
						return nil, fmt.Errorf("skill.view 参数不是合法 JSON：%w", err)
					}
				}
				s, err := l.View(in.Name)
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
			},
		},
		{
			Name:     "skill.read",
			Title:    "读取技能引用文件",
			Summary:  "读技能目录内的相对路径文件，越界访问会被拒绝",
			Tags:     []string{"skill"},
			Risk:     plugin.RiskRead,
			Schema:   json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"技能名"},"path":{"type":"string","description":"技能目录内的相对路径，如 references/checklist.md"}},"required":["name","path"]}`),
			Examples: []string{`{"name":"premarket-plan","path":"references/checklist.md"}`},
			Run: func(ctx context.Context, args json.RawMessage) (any, error) {
				var in struct {
					Name string `json:"name"`
					Path string `json:"path"`
				}
				if len(args) > 0 {
					if err := json.Unmarshal(args, &in); err != nil {
						return nil, fmt.Errorf("skill.read 参数不是合法 JSON：%w", err)
					}
				}
				buf, err := l.Read(in.Name, in.Path)
				if err != nil {
					return nil, err
				}
				return map[string]any{
					"name":    in.Name,
					"path":    filepath.ToSlash(in.Path),
					"content": string(buf),
					"chars":   utf8.RuneCount(buf),
				}, nil
			},
		},
	}
}
