package datasource

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// 这个文件只做一件事：只读解析 NSKeyedArchiver（binary plist）。
//
// 为什么自己写而不是引第三方：整个解析只用到 bplist 的一个极小面（UID/字典/数组/
// 整数/二进制数据/ASCII+UTF16 字符串），引一个通用 plist 库会为了几行代码带进整包依赖；
// 同时上游数据是用户本机的同花顺文件，自己实现也方便逐条加边界检查，读到畸形数据直接报错。
//
// 参考格式：Apple "Binary Property List 1.0"（bplist00），trailer 32 字节，
// 末尾依次是 offsetIntSize/objRefSize/numObjects/topObject/offsetTableOffset。

const bplistMagic = "bplist00"

// plistUID 是 bplist 的 UID 对象（NSKeyedArchiver 用它指向 $objects 下标）。
type plistUID int

type bplistReader struct {
	data     []byte
	offsets  []int
	refSize  int
	topIndex int
}

// parseBplist 解析整个归档，返回根对象。
func parseBplist(data []byte) (any, error) {
	reader, err := newBplistReader(data)
	if err != nil {
		return nil, err
	}
	return reader.object(reader.topIndex, 0)
}

func newBplistReader(data []byte) (*bplistReader, error) {
	if len(data) < len(bplistMagic)+32 || string(data[:len(bplistMagic)]) != bplistMagic {
		return nil, errors.New("datasource: 不是 binary plist（缺少 bplist00 头）")
	}
	trailer := data[len(data)-32:]
	offsetSize := int(trailer[6])
	refSize := int(trailer[7])
	numObjects := int(binary.BigEndian.Uint64(trailer[8:16]))
	topIndex := int(binary.BigEndian.Uint64(trailer[16:24]))
	tableStart := int(binary.BigEndian.Uint64(trailer[24:32]))
	if offsetSize < 1 || offsetSize > 8 || refSize < 1 || refSize > 8 {
		return nil, errors.New("datasource: binary plist 偏移/引用宽度非法")
	}
	if numObjects <= 0 || topIndex < 0 || topIndex >= numObjects {
		return nil, errors.New("datasource: binary plist 对象数量非法")
	}
	if tableStart < 0 || tableStart+numObjects*offsetSize > len(data) {
		return nil, errors.New("datasource: binary plist 偏移表越界")
	}
	offsets := make([]int, numObjects)
	for i := 0; i < numObjects; i++ {
		offset := 0
		base := tableStart + i*offsetSize
		for k := 0; k < offsetSize; k++ {
			offset = offset<<8 | int(data[base+k])
		}
		if offset <= 0 || offset >= len(data) {
			return nil, errors.New("datasource: binary plist 对象偏移非法")
		}
		offsets[i] = offset
	}
	return &bplistReader{data: data, offsets: offsets, refSize: refSize, topIndex: topIndex}, nil
}

func (r *bplistReader) object(index, depth int) (any, error) {
	if depth > 32 {
		return nil, errors.New("datasource: binary plist 嵌套过深")
	}
	if index < 0 || index >= len(r.offsets) {
		return nil, errors.New("datasource: binary plist 对象下标越界")
	}
	return r.at(r.offsets[index], depth+1)
}

// length 解析「长度」字段：低 4 位为 0xF 时，真实长度在后随的整数对象里。
func (r *bplistReader) length(offset, info int) (int, int, error) {
	if info != 0x0F {
		return info, offset + 1, nil
	}
	if offset+1 >= len(r.data) {
		return 0, 0, errors.New("datasource: binary plist 长度对象缺失")
	}
	marker := r.data[offset+1]
	if marker>>4 != 0x1 {
		return 0, 0, errors.New("datasource: binary plist 长度对象不是整数")
	}
	size := 1 << (marker & 0x0F)
	start := offset + 2
	if start+size > len(r.data) {
		return 0, 0, errors.New("datasource: binary plist 长度越界")
	}
	value := 0
	for i := 0; i < size; i++ {
		value = value<<8 | int(r.data[start+i])
	}
	return value, start + size, nil
}

func (r *bplistReader) at(offset, depth int) (any, error) {
	if offset < 0 || offset >= len(r.data) {
		return nil, errors.New("datasource: binary plist 偏移越界")
	}
	marker := r.data[offset]
	kind := marker >> 4
	info := int(marker & 0x0F)
	switch kind {
	case 0x0:
		if marker == 0x09 {
			return true, nil
		}
		if marker == 0x08 {
			return false, nil
		}
		return nil, nil
	case 0x1:
		size := 1 << info
		if offset+1+size > len(r.data) {
			return nil, errors.New("datasource: binary plist 整数越界")
		}
		value := int64(0)
		for i := 0; i < size; i++ {
			value = value<<8 | int64(r.data[offset+1+i])
		}
		return value, nil
	case 0x2:
		size := 1 << info
		if offset+1+size > len(r.data) {
			return nil, errors.New("datasource: binary plist 浮点越界")
		}
		raw := r.data[offset+1 : offset+1+size]
		switch size {
		case 4:
			return float64(math.Float32frombits(binary.BigEndian.Uint32(raw))), nil
		case 8:
			return math.Float64frombits(binary.BigEndian.Uint64(raw)), nil
		}
		return nil, nil
	case 0x4:
		count, body, err := r.length(offset, info)
		if err != nil {
			return nil, err
		}
		if body+count > len(r.data) {
			return nil, errors.New("datasource: binary plist 二进制数据越界")
		}
		data := make([]byte, count)
		copy(data, r.data[body:body+count])
		return data, nil
	case 0x5:
		count, body, err := r.length(offset, info)
		if err != nil {
			return nil, err
		}
		if body+count > len(r.data) {
			return nil, errors.New("datasource: binary plist 字符串越界")
		}
		return string(r.data[body : body+count]), nil
	case 0x6:
		count, body, err := r.length(offset, info)
		if err != nil {
			return nil, err
		}
		if body+count*2 > len(r.data) {
			return nil, errors.New("datasource: binary plist UTF-16 字符串越界")
		}
		units := make([]uint16, count)
		for i := 0; i < count; i++ {
			units[i] = binary.BigEndian.Uint16(r.data[body+i*2:])
		}
		return string(utf16Decode(units)), nil
	case 0x8:
		size := info + 1
		if offset+1+size > len(r.data) {
			return nil, errors.New("datasource: binary plist UID 越界")
		}
		value := 0
		for i := 0; i < size; i++ {
			value = value<<8 | int(r.data[offset+1+i])
		}
		return plistUID(value), nil
	case 0xA:
		count, body, err := r.length(offset, info)
		if err != nil {
			return nil, err
		}
		items := make([]any, 0, count)
		for i := 0; i < count; i++ {
			ref, refErr := r.reference(body + i*r.refSize)
			if refErr != nil {
				return nil, refErr
			}
			item, itemErr := r.object(ref, depth+1)
			if itemErr != nil {
				return nil, itemErr
			}
			items = append(items, item)
		}
		return items, nil
	case 0xD:
		count, body, err := r.length(offset, info)
		if err != nil {
			return nil, err
		}
		keys := make([]string, 0, count)
		for i := 0; i < count; i++ {
			ref, refErr := r.reference(body + i*r.refSize)
			if refErr != nil {
				return nil, refErr
			}
			key, keyErr := r.object(ref, depth+1)
			if keyErr != nil {
				return nil, keyErr
			}
			if text, ok := key.(string); ok {
				keys = append(keys, text)
			}
		}
		valuesStart := body + count*r.refSize
		dict := make(map[string]any, count)
		for i, key := range keys {
			ref, refErr := r.reference(valuesStart + i*r.refSize)
			if refErr != nil {
				return nil, refErr
			}
			value, valueErr := r.object(ref, depth+1)
			if valueErr != nil {
				return nil, valueErr
			}
			dict[key] = value
		}
		return dict, nil
	}
	return nil, fmt.Errorf("datasource: 不支持的 binary plist 对象类型 0x%x", marker)
}

func (r *bplistReader) reference(offset int) (int, error) {
	if offset < 0 || offset+r.refSize > len(r.data) {
		return 0, errors.New("datasource: binary plist 引用越界")
	}
	value := 0
	for i := 0; i < r.refSize; i++ {
		value = value<<8 | int(r.data[offset+i])
	}
	return value, nil
}

// utf16Decode 把 UTF-16 码元解成 Go 字符串（含代理对合并）。
func utf16Decode(units []uint16) []rune {
	out := make([]rune, 0, len(units))
	for i := 0; i < len(units); i++ {
		unit := units[i]
		if unit >= 0xD800 && unit <= 0xDBFF && i+1 < len(units) {
			low := units[i+1]
			if low >= 0xDC00 && low <= 0xDFFF {
				out = append(out, rune(0x10000+(int32(unit-0xD800)<<10)+int32(low-0xDC00)))
				i++
				continue
			}
		}
		out = append(out, rune(unit))
	}
	return out
}

// decodeKeyedArchive 解 NSKeyedArchiver，返回 $top.root 指向的实体字典。
// 同花顺的分组归档根实体形如 {self.value: <bytes>, self.ID: <bytes>, ...}。
func decodeKeyedArchive(data []byte) (map[string]any, error) {
	root, err := parseBplist(data)
	if err != nil {
		return nil, err
	}
	archive, ok := root.(map[string]any)
	if !ok {
		return nil, errors.New("datasource: 归档根对象不是字典")
	}
	objects, ok := archive["$objects"].([]any)
	if !ok {
		return nil, errors.New("datasource: 归档缺少 $objects")
	}
	top, ok := archive["$top"].(map[string]any)
	if !ok {
		return nil, errors.New("datasource: 归档缺少 $top")
	}
	ref, ok := top["root"].(plistUID)
	if !ok {
		return nil, errors.New("datasource: 归档 $top.root 不是 UID")
	}
	resolved := resolveUID(objects, ref, 0)
	entity, ok := resolved.(map[string]any)
	if !ok {
		return nil, errors.New("datasource: 归档根实体不是字典")
	}
	return entity, nil
}

// resolveUID 递归把 UID、字典、数组里的引用换成实际对象。
func resolveUID(objects []any, value any, depth int) any {
	if depth > 32 {
		return nil
	}
	switch item := value.(type) {
	case plistUID:
		index := int(item)
		if index < 0 || index >= len(objects) {
			return nil
		}
		return resolveUID(objects, objects[index], depth+1)
	case map[string]any:
		out := make(map[string]any, len(item))
		for k, v := range item {
			out[k] = resolveUID(objects, v, depth+1)
		}
		return out
	case []any:
		out := make([]any, 0, len(item))
		for _, v := range item {
			out = append(out, resolveUID(objects, v, depth+1))
		}
		return out
	}
	return value
}
