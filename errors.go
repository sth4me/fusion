// Package fusion 的错误处理。
//
// 查询无结果：One() 查无结果直接返回标准库 sql.ErrNoRows，fusion 不另造 sentinel。
// 用 errors.Is(err, sql.ErrNoRows) 判断即可（标准库惯用法）。
package fusion
