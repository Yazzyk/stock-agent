package llm

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// 前端按 camelCase 取字段，这里把 presets 的 JSON 契约钉死：任何改名都会先让测试红。
func TestPresetJSONFieldNames(t *testing.T) {
	cases := []struct {
		name string
		typ  reflect.Type
		want []string
	}{
		{
			name: "ModelPreset",
			typ:  reflect.TypeOf(ModelPreset{}),
			want: []string{"model", "label", "contextWindow", "maxOutput", "defaultMaxTokens", "note"},
		},
		{
			name: "ProviderPreset",
			typ:  reflect.TypeOf(ProviderPreset{}),
			want: []string{"provider", "label", "baseUrl", "keyUrl", "docsUrl", "authMode", "models", "note"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := make([]string, 0, c.typ.NumField())
			for i := 0; i < c.typ.NumField(); i++ {
				tag := c.typ.Field(i).Tag.Get("json")
				if tag == "" || tag == "-" {
					t.Fatalf("字段 %s 缺少 json tag", c.typ.Field(i).Name)
				}
				name := strings.Split(tag, ",")[0]
				if strings.Contains(name, "_") {
					t.Fatalf("字段 %s 用了 snake_case: %q（前端按 camelCase 取）", c.typ.Field(i).Name, name)
				}
				got = append(got, name)
			}
			want := append([]string(nil), c.want...)
			sort.Strings(got)
			sort.Strings(want)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("JSON 字段名不符: got %v, want %v", got, want)
			}
		})
	}
}

// 实际序列化结果也要逐字核对：omitempty 字段在有值时必须出现，且不得出现契约外字段。
func TestPresetsMarshalShape(t *testing.T) {
	raw, err := json.Marshal(Presets())
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("presets 为空")
	}
	if err := assertKeys(items[0], []string{"provider", "label", "baseUrl", "keyUrl", "docsUrl", "models"}); err != nil {
		t.Fatalf("provider 字段: %v", err)
	}
	models, _ := items[0]["models"].([]any)
	if len(models) == 0 {
		t.Fatal("deepseek 缺少模型")
	}
	first, _ := models[0].(map[string]any)
	if err := assertKeys(first, []string{"model", "label", "contextWindow", "maxOutput", "defaultMaxTokens", "note"}); err != nil {
		t.Fatalf("model 字段: %v", err)
	}
	if first["model"] != "deepseek-flash" {
		t.Fatalf("首个模型应为 deepseek-flash: %v", first["model"])
	}
	if snake := findSnakeCase(items); snake != "" {
		t.Fatalf("出现 snake_case 字段: %q", snake)
	}
}

// authMode 只在 opencode / workbuddy 上出现：其他 provider 的 omitempty 不得输出空字段。
func TestPresetsMarshalAuthMode(t *testing.T) {
	raw, err := json.Marshal(Presets())
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	for _, item := range items {
		provider, _ := item["provider"].(string)
		got, has := item["authMode"]
		want := provider == "opencode" || provider == "workbuddy"
		if has != want {
			t.Fatalf("%s 的 authMode 出现状态不符: has=%v want=%v", provider, has, want)
		}
		if want && got != provider {
			t.Fatalf("%s 的 authMode = %v, want %q", provider, got, provider)
		}
	}
}

// assertKeys 要求必有字段齐全，且不出现契约外字段（多字段是契约漂移，前端不会读到）。
func assertKeys(obj map[string]any, want []string) error {
	allowed := map[string]bool{}
	for _, k := range want {
		allowed[k] = true
		if _, ok := obj[k]; !ok {
			return fmt.Errorf("缺少字段 %q（实际: %s）", k, strings.Join(sortedKeys(obj), ","))
		}
	}
	for k := range obj {
		if !allowed[k] {
			return fmt.Errorf("出现契约外字段 %q（允许: %s）", k, strings.Join(want, ","))
		}
	}
	return nil
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func findSnakeCase(v any) string {
	switch t := v.(type) {
	case map[string]any:
		for k, sub := range t {
			if strings.Contains(k, "_") {
				return k
			}
			if got := findSnakeCase(sub); got != "" {
				return got
			}
		}
	case []any:
		for _, sub := range t {
			if got := findSnakeCase(sub); got != "" {
				return got
			}
		}
	}
	return ""
}
