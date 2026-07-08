// Package model 定义数据模型并持有全局 Table 变量。
//
// 这是 fusion 推荐的项目结构：
//   - 模型类型（User）和它的 Table 变量（Users）放同一个包
//   - Table 变量是导出的，业务层（service/handler）通过 import model 拿到
//   - 注册集中在 RegisterAll()，由 main 启动时显式调用一次（不放 init，避免 import 副作用）
//
// 这样依赖方向单向：service → model → fusion，无循环。
// model 包是依赖链叶子节点，任何上层都能 import 它拿 Table 变量。
package model

import (
	"github.com/sth4me/fusion"
	"github.com/sth4me/fusion/col"
	"github.com/sth4me/fusion/meta"
)

// User 是全包装模型：所有字段都是 col.Col[T]。
// Col[*string] 表示可空字段（nil = NULL）。
type User struct {
	ID    col.Col[int64]
	Name  col.Col[string]
	Age   col.Col[int]
	Email col.Col[*string]
}

// 全局 Table 变量：业务层通过 model.Users 引用。
// 初始为 nil，RegisterAll() 调用后才被赋值。
// 放这里（而非 main）是因为它是 User 类型的伴生元信息，
// 归属 model 包最自然，且 model 作为叶子节点不会造成循环导入。
var (
	Users *meta.Table[User]
)

// RegisterAll 注册所有模型与关联。在 main() 启动早期调用一次。
//
// 集中注册的好处：
//   - 顺序可控（先模型后关联；关联要求子类型已注册）
//   - 无 init() 副作用（仅 import model 不触发注册）
//   - 便于审计和按环境传不同表名
func RegisterAll() {
	Users = fusion.Register[User]("users")

	// 关联注册也在此（示例无关联，真实项目里 fusion.HasMany/BelongsTo 写这里）。
	// 注意顺序：先 Register 所有涉及的类型，再注册关联（关联 picker 引用同包类型）。
}
