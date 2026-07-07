package builder

import (
	"strings"

	"github.com/sth4me/fusion/dialect"
	"github.com/sth4me/fusion/meta"
)

// CompoundOp 是集合操作类型。
type CompoundOp string

const (
	OpUnion     CompoundOp = "UNION"
	OpUnionAll  CompoundOp = "UNION ALL"
	OpIntersect CompoundOp = "INTERSECT"
	OpExcept    CompoundOp = "EXCEPT"
)

// CompoundArm 是复合查询的一条 SELECT 臂。
type CompoundArm struct {
	Meta   *meta.ModelMeta // 主表元信息（渲染 FROM/列）
	Query  SelectQuery     // 该臂的 SELECT 配置
}

// CompoundQuery 描述一个集合复合查询（UNION/INTERSECT/EXCEPT）。
//
// 结构：arm1 OP arm2 OP ... 尾部统一的 ORDER BY / LIMIT / OFFSET / 锁子句。
// 每条 arm 用 buildSelectBody 渲染（自身可含 WHERE/GROUP BY 等，但不应自带
// 影响整体语义的 ORDER/LIMIT —— 由 Tail 统一控制）。
type CompoundQuery struct {
	Arms []CompoundArm
	Ops  []CompoundOp // Arms[i] 与 Arms[i+1] 之间的操作符，长度 = len(Arms)-1
	// Tail：作用于整体的尾部子句
	Orders     []OrderItem
	Limit      int
	Offset     int
	LockClause string
}

// BuildCompound 渲染复合查询，返回 SQL 与参数。
// 每条 arm 渲染为 (SELECT ...)，中间用对应 OP 连接，尾部追加统一的 ORDER/LIMIT/OFFSET/锁。
func BuildCompound(cq CompoundQuery, d dialect.Dialect) (string, []any) {
	// 防御：操作符数不匹配时默认 UNION 填充
	if len(cq.Arms) > 1 && len(cq.Ops) != len(cq.Arms)-1 {
		cq.Ops = make([]CompoundOp, len(cq.Arms)-1)
		for i := range cq.Ops {
			cq.Ops[i] = OpUnion
		}
	}
	// 单一 renderer 顺序渲染各 arm，保证占位符编号连续。
	aliasMap := map[string]string{}
	r := &renderer{d: d, aliasMap: aliasMap}

	parts := make([]string, 0, len(cq.Arms))
	for _, arm := range cq.Arms {
		// 每条 arm 重置 aliasMap（避免跨 arm 串）
		for k := range aliasMap {
			delete(aliasMap, k)
		}
		if arm.Query.Alias != "" && arm.Meta != nil {
			aliasMap[arm.Meta.Table] = arm.Query.Alias
		}
		for _, j := range arm.Query.Joins {
			if j.Alias != "" {
				aliasMap[j.Table] = j.Alias
			}
		}
		// arm 不渲染尾部 ORDER/LIMIT/OFFSET/锁（由整体 Tail 控制）：拷贝并清零
		armQ := arm.Query
		armQ.Orders = nil
		armQ.Limit = 0
		armQ.Offset = 0
		armQ.LockClause = ""
		body := buildSelectBody(r, arm.Meta, armQ, d)
		// 不加括号：SQLite 不接受 (SELECT) UNION (SELECT)，而 PG/MySQL/SQLite 都
		// 接受无括号的 SELECT ... UNION SELECT ... 且尾部 ORDER/LIMIT 作用于整体
		// （我们已清零 arm 的尾部子句，无歧义）。
		parts = append(parts, body)
	}

	// 用每对 arm 对应的 op 拼接
	var b strings.Builder
	for i, p := range parts {
		if i > 0 {
			b.WriteString(" " + string(cq.Ops[i-1]) + " ")
		}
		b.WriteString(p)
	}
	sqlStr := b.String()

	// 尾部 ORDER BY（作用于整体）
	if len(cq.Orders) > 0 {
		orderParts := make([]string, 0, len(cq.Orders))
		for _, o := range cq.Orders {
			orderParts = append(orderParts, o.RenderClause(r))
		}
		sqlStr += " ORDER BY " + strings.Join(orderParts, ", ")
	}
	if cq.Limit > 0 {
		sqlStr += " LIMIT " + d.Placeholder(r.phIdx+1)
		r.phIdx++
		r.args = append(r.args, cq.Limit)
	}
	if cq.Offset > 0 {
		sqlStr += " OFFSET " + d.Placeholder(r.phIdx+1)
		r.phIdx++
		r.args = append(r.args, cq.Offset)
	}
	if cq.LockClause != "" {
		sqlStr += " " + cq.LockClause
	}
	return sqlStr, r.args
}
