package dialect

import "strings"

// quote 用指定字符包裹标识符，并将标识符内出现的该字符双写转义。
func quote(name string, ch byte) string {
	// 已引用则原样返回
	if len(name) >= 2 && name[0] == ch && name[len(name)-1] == ch {
		return name
	}
	b := make([]byte, 0, len(name)+2)
	b = append(b, ch)
	for i := 0; i < len(name); i++ {
		if name[i] == ch {
			b = append(b, ch, ch) // 双写转义
			continue
		}
		b = append(b, name[i])
	}
	b = append(b, ch)
	return string(b)
}

// quoteMaybeSchema 引用可能含 schema 前缀的表名（schema.table）。
func quoteMaybeSchema(name string, d Dialect) string {
	if i := strings.IndexByte(name, '.'); i >= 0 {
		return d.QuoteIdent(name[:i]) + "." + d.QuoteIdent(name[i+1:])
	}
	return d.QuoteIdent(name)
}

// joinCSV 用 ", " 连接字符串切片。
func joinCSV(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out += ", " + p
	}
	return out
}

// itoa 将非负整数转为字符串（避免引入 strconv 的开销在热路径）。
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
