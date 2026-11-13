package datasource

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

var errBadPayload = errors.New("datasource: 响应不是合法 JSON/JSONP")

// unwrapJSONP 剥掉 JSONP 外壳（quotebridge_x({...});），纯 JSON 原样返回。
func unwrapJSONP(body []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, errBadPayload
	}
	if trimmed[0] == '{' || trimmed[0] == '[' {
		return trimmed, nil
	}
	text := string(trimmed)
	start := strings.Index(text, "(")
	end := strings.LastIndex(text, ")")
	if start < 0 || end <= start {
		return nil, errBadPayload
	}
	inner := strings.TrimSpace(text[start+1 : end])
	if inner == "" {
		return nil, errBadPayload
	}
	return []byte(inner), nil
}

// parseNum 解析接口里的数值字段：空串、"--"、"-" 都当作缺失。
func parseNum(s string) (float64, bool) {
	text := strings.TrimSpace(s)
	switch text {
	case "", "-", "--", "null", "None":
		return 0, false
	}
	v, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// numOrZero 是 parseNum 的零值兜底版本（用于可选字段）。
func numOrZero(s string) float64 {
	if v, ok := parseNum(s); ok {
		return v
	}
	return 0
}

// decodeGBK 把 GB18030/GBK 字节流转成 UTF-8。
// 腾讯 qt.gtimg.cn 与新浪 hq.sinajs.cn 都返回 GBK，不转码会得到乱码名称。
func decodeGBK(raw []byte) (string, error) {
	decoded, err := simplifiedchinese.GB18030.NewDecoder().Bytes(raw)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(decoded) {
		return "", errors.New("datasource: GBK 解码后不是合法 UTF-8")
	}
	return string(decoded), nil
}

// anyNum 把 JSON 里的数字/字符串数字/"-" 统一成 float64；无法解析一律 0（缺失）。
// 上游对停牌/无数据的字段会混用数字、"-"、null 三种形态，强类型一崩就是整批失败，
// 所以解析一律先归一成 any 再取值。
func anyNum(v any) float64 {
	switch value := v.(type) {
	case float64:
		return value
	case json.Number:
		if f, err := value.Float64(); err == nil {
			return f
		}
	case string:
		if f, ok := parseNum(value); ok {
			return f
		}
	}
	return 0
}

// anyStr 取 JSON 字符串值；非字符串返回空串。
func anyStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// splitNonEmpty 按分隔符切分并丢弃空段，便于解析 "a~b~~c" 这类定长字段串。
func splitNonEmpty(s, sep string) []string {
	parts := strings.Split(s, sep)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
