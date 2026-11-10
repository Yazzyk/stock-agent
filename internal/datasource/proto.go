package datasource

// 同花顺本地的分组归档没有 .proto，只能按 protobuf wire 格式取字段：
// 每个字段 = varint(tag<<3|wireType) + 载荷。
// 这里只实现只读的最小解析器（varint / 64bit / length-delimited / 32bit），
// 遇到尾部残缺或非法 wireType 就停止，返回已解析出的字段，绝不 panic。
const (
	protoWireVarint = 0
	protoWire64     = 1
	protoWireBytes  = 2
	protoWire32     = 5
)

type protoField struct {
	Number   int
	WireType int
	Varint   uint64
	Bytes    []byte
}

func parseProtoFields(data []byte) []protoField {
	fields := make([]protoField, 0, 8)
	position := 0
	for position < len(data) {
		tag, next := readVarint(data, position)
		if next <= 0 {
			return fields
		}
		position = next
		number := int(tag >> 3)
		wireType := int(tag & 0x7)
		if number <= 0 {
			return fields
		}
		switch wireType {
		case protoWireVarint:
			value, after := readVarint(data, position)
			if after <= 0 {
				return fields
			}
			fields = append(fields, protoField{Number: number, WireType: wireType, Varint: value})
			position = after
		case protoWire64:
			if position+8 > len(data) {
				return fields
			}
			position += 8
		case protoWireBytes:
			size, after := readVarint(data, position)
			if after <= 0 {
				return fields
			}
			end := after + int(size)
			if end > len(data) || end < after {
				return fields
			}
			fields = append(fields, protoField{Number: number, WireType: wireType, Bytes: data[after:end]})
			position = end
		case protoWire32:
			if position+4 > len(data) {
				return fields
			}
			position += 4
		default:
			// 3/4 是已废弃的 group，真实数据里不出现；遇到就停，保留已解析内容。
			return fields
		}
	}
	return fields
}

// readVarint 读一个 varint，返回 (值, 下一个位置)；非法返回 (0, -1)。
func readVarint(data []byte, position int) (uint64, int) {
	value := uint64(0)
	shift := uint(0)
	for i := position; i < len(data); i++ {
		current := data[i]
		value |= uint64(current&0x7f) << shift
		if current&0x80 == 0 {
			return value, i + 1
		}
		shift += 7
		if shift > 63 {
			return 0, -1
		}
	}
	return 0, -1
}
