package datasource

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// 这些 .dat 是从本机同花顺容器里复制出来的真实分组归档（只含分组名与 6 位代码，
// 不含 userid/凭证；userid 归档故意没有复制）。
func TestParseTHSDirRealFiles(t *testing.T) {
	groups, userID, err := parseTHSDir(filepath.Join("testdata", "ths_local"))
	if err != nil {
		t.Fatalf("解析真实 .dat 失败: %v", err)
	}
	if userID != "" {
		t.Fatalf("fixture 里不应有 userid，实际 %q", userID)
	}
	if len(groups) == 0 {
		t.Fatal("应至少解析出一个自选分组")
	}
	total := 0
	cjkGroups := 0
	for _, group := range groups {
		if strings.TrimSpace(group.Name) == "" {
			t.Fatalf("分组名为空: %+v", group)
		}
		// GBK→UTF-8 的关键断言：必须合法 UTF-8、不含替换字符、且真的有中文
		//（不断言具体分组名，fixture 更新时不会误报）
		if !utf8.ValidString(group.Name) {
			t.Fatalf("分组名不是合法 UTF-8: %q", group.Name)
		}
		if strings.ContainsRune(group.Name, '\uFFFD') {
			t.Fatalf("分组名出现替换字符（GBK 未解码）: %q", group.Name)
		}
		for _, r := range group.Name {
			if r >= 0x4E00 && r <= 0x9FFF {
				cjkGroups++
				break
			}
		}
		total += len(group.Codes)
		for _, code := range group.Codes {
			if len(code) != 8 {
				t.Fatalf("代码 %q 不是 8 位（市场前缀+6 位）", code)
			}
			prefix := code[:2]
			if prefix != "sh" && prefix != "sz" && prefix != "bj" {
				t.Fatalf("代码 %q 市场前缀非法", code)
			}
			for _, r := range code[2:] {
				if r < '0' || r > '9' {
					t.Fatalf("代码 %q 含非数字", code)
				}
			}
		}
	}
	if total == 0 {
		t.Fatal("分组里应至少有一个成分股")
	}
	if cjkGroups == 0 {
		t.Fatal("至少应有一个分组名是中文（GBK 未解码时会退化成替换字符或纯 ASCII）")
	}
	t.Logf("解析出 %d 个分组、%d 只标的；首个分组: %q %v", len(groups), total, groups[0].Name, groups[0].Codes)
}

func TestParseTHSDirNotFound(t *testing.T) {
	if _, _, err := parseTHSDir(filepath.Join(t.TempDir(), "not-exist")); err == nil {
		t.Fatal("目录不存在应报错")
	}
}

func TestParseTHSDirEmpty(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := parseTHSDir(dir); err == nil {
		t.Fatal("空目录应报错")
	}
	// 目录里有文件但不是归档：跳过而不是 panic
	if err := os.WriteFile(filepath.Join(dir, "blockstock_1__public__CCk=.dat"), []byte("not a plist"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := parseTHSDir(dir); err == nil {
		t.Fatal("非法归档应返回错误而不是崩溃")
	}
}

func TestDecodeKeyedArchiveGarbage(t *testing.T) {
	if _, err := decodeKeyedArchive([]byte("bplist00-short")); err == nil {
		t.Fatal("截断的 plist 应报错")
	}
	if _, err := decodeKeyedArchive([]byte("not a plist at all")); err == nil {
		t.Fatal("非 plist 应报错")
	}
}

func TestDecodeTHSBase64Text(t *testing.T) {
	// GBK 编码的「医药」= D2 BD D2 A9，这是最容易踩的坑（它恰好也是合法 UTF-8 字节）
	raw := []byte(base64.StdEncoding.EncodeToString([]byte{0xD2, 0xBD, 0xD2, 0xA9}))
	if got := decodeTHSBase64Text(raw); got != "医药" {
		t.Fatalf("decodeTHSBase64Text = %q，期望 医药", got)
	}
	// GBK 优先是有意的取舍：UTF-8 中文（如「科技」的 E7A791 E68A80）在 GBK 视角下
	// 同样是合法字符，无法可靠区分；同花顺实测就是 GBK，所以不做 UTF-8 猜测。
	// 这里只保证不会 panic 或返回替换字符。
	utf8Text := []byte(base64.StdEncoding.EncodeToString([]byte("科技")))
	if got := decodeTHSBase64Text(utf8Text); strings.ContainsRune(got, '\uFFFD') {
		t.Fatalf("不应出现替换字符: %q", got)
	}
	if got := decodeTHSBase64Text([]byte("!!!")); got != "" {
		t.Fatalf("非法 base64 应返回空串，实际 %q", got)
	}
}

func TestSplitTHSCodeString(t *testing.T) {
	codes := splitTHSCodeString("600000|000001|,17|33|300750|")
	want := []string{"600000", "000001", "300750"}
	if len(codes) != len(want) {
		t.Fatalf("拆分结果 = %v，期望 %v", codes, want)
	}
	for i := range want {
		if codes[i] != want[i] {
			t.Fatalf("拆分结果[%d] = %q，期望 %q", i, codes[i], want[i])
		}
	}
	// 市场码（17/33）必须被丢弃；板块指数（883/884/885）在这一层仍是 6 位数字，
	// 由下游 thsCodeToCode 过滤（见 TestTHSCodeToCode），拆分器只负责切字段。
	codes = splitTHSCodeString("883307|,17|33|")
	if len(codes) != 1 || codes[0] != "883307" {
		t.Fatalf("拆分器应只保留 6 位数字段，实际 %v", codes)
	}
	if _, ok := thsCodeToCode("883307"); ok {
		t.Fatal("板块指数不应被转成可交易代码")
	}
}

func TestTHSCodeToCode(t *testing.T) {
	cases := []struct {
		raw  string
		ok   bool
		msym string
	}{
		{"600000", true, "sh600000"},
		{"000001", true, "sz000001"},
		{"300750", true, "sz300750"},
		{"430047", true, "bj430047"},
		{"510300", true, "sh510300"},
		{"883307", false, ""},
		{"884201", false, ""},
		{"881101", false, ""},
		{"60000", false, ""},
		{"abcdef", false, ""},
	}
	for _, tc := range cases {
		code, ok := thsCodeToCode(tc.raw)
		if ok != tc.ok {
			t.Fatalf("thsCodeToCode(%q) ok = %v，期望 %v", tc.raw, ok, tc.ok)
		}
		if ok && code.MarketSymbol() != tc.msym {
			t.Fatalf("thsCodeToCode(%q) = %q，期望 %q", tc.raw, code.MarketSymbol(), tc.msym)
		}
	}
}

func TestParseTHSGroupOrder(t *testing.T) {
	// field1 = "p29,14B,146"，首个 token 带非十六进制前缀，需剥掉
	payload := encodeProtoBytes(1, []byte("p29,14B,146"))
	got := parseTHSGroupOrder(payload)
	want := []int{0x29, 0x14B, 0x146}
	if len(got) != len(want) {
		t.Fatalf("顺序表 = %v，期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("顺序表[%d] = %d，期望 %d", i, got[i], want[i])
		}
	}
	if got := parseTHSGroupOrder(nil); got != nil {
		t.Fatalf("空载荷应返回 nil，实际 %v", got)
	}
}

func TestSortTHSGroups(t *testing.T) {
	groups := []thsGroup{
		{ExternalID: "300", Name: "C"},
		{ExternalID: "100", Name: "A"},
		{ExternalID: "200", Name: "B"},
	}
	sortTHSGroups(groups, []int{200, 100, 300})
	want := []string{"B", "A", "C"}
	for i, name := range want {
		if groups[i].Name != name {
			t.Fatalf("第 %d 个分组 = %q，期望 %q（顺序表优先）", i, groups[i].Name, name)
		}
	}
	// 不在顺序表里的按 ID 升序排后面
	groups = []thsGroup{{ExternalID: "9", Name: "X"}, {ExternalID: "2", Name: "Y"}}
	sortTHSGroups(groups, []int{100})
	if groups[0].Name != "Y" || groups[1].Name != "X" {
		t.Fatalf("无顺序信息时应按 ID 升序: %+v", groups)
	}
}

func TestParseProtoFields(t *testing.T) {
	// field1 = length-delimited "abc"，field2 = varint 300
	payload := append(encodeProtoBytes(1, []byte("abc")), encodeProtoVarint(2, 300)...)
	fields := parseProtoFields(payload)
	if len(fields) != 2 {
		t.Fatalf("字段数 = %d，期望 2: %+v", len(fields), fields)
	}
	if fields[0].Number != 1 || string(fields[0].Bytes) != "abc" {
		t.Fatalf("字段 0 = %+v", fields[0])
	}
	if fields[1].Number != 2 || fields[1].Varint != 300 {
		t.Fatalf("字段 1 = %+v", fields[1])
	}

	// 截断的载荷：返回已解析字段而不是 panic
	if got := parseProtoFields([]byte{0x0A, 0x05, 0x61}); len(got) != 0 {
		t.Fatalf("截断载荷应返回空，实际 %+v", got)
	}
	// 非法 wireType(3) 停止解析
	if got := parseProtoFields([]byte{0x0B}); len(got) != 0 {
		t.Fatalf("非法 wireType 应停止，实际 %+v", got)
	}
}

func TestTHSArchiveKey(t *testing.T) {
	cases := []struct{ in, want string }{
		{"blockstock_631110168__public__CCk=.dat", "CCk="},
		{"CCk=.dat", "CCk="},
	}
	for _, tc := range cases {
		if got := thsArchiveKey(tc.in); got != tc.want {
			t.Fatalf("thsArchiveKey(%q) = %q，期望 %q", tc.in, got, tc.want)
		}
	}
}

func TestTHSUserIDFromDir(t *testing.T) {
	if got := thsUserIDFromDir("/Users/x/Library/cloud_store/blockstock_631110168/public"); got != "631110168" {
		t.Fatalf("userid = %q，期望 631110168", got)
	}
	if got := thsUserIDFromDir("/tmp/none"); got != "" {
		t.Fatalf("无 blockstock 目录应返回空，实际 %q", got)
	}
}

func TestScanTHSLocalMissing(t *testing.T) {
	home := t.TempDir()
	_, _, err := scanTHSLocal(home)
	if err == nil {
		t.Fatal("没有同花顺容器应报错")
	}
	if !contains(err.Error(), "同花顺") {
		t.Fatalf("错误文案应说明同花顺缺失: %v", err)
	}
}

// encodeProtoVarint 构造 field(number, varint) 的 wire 字节。
func encodeProtoVarint(number int, value uint64) []byte {
	out := encodeVarint(uint64(number)<<3 | protoWireVarint)
	return append(out, encodeVarint(value)...)
}

// encodeProtoBytes 构造 length-delimited 字段的 wire 字节。
func encodeProtoBytes(number int, payload []byte) []byte {
	out := encodeVarint(uint64(number)<<3 | protoWireBytes)
	out = append(out, encodeVarint(uint64(len(payload)))...)
	return append(out, payload...)
}

func encodeVarint(value uint64) []byte {
	out := make([]byte, 0, 10)
	for value >= 0x80 {
		out = append(out, byte(value)|0x80)
		value >>= 7
	}
	return append(out, byte(value))
}
