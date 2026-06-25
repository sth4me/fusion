package fusion

import "fusion/query"

// 哨兵错误（sentinel errors），支持 errors.Is 判断。
// 业务代码可用 errors.Is(err, fusion.ErrNotFound) 区分"无结果"与"查询错误"。

// ErrNotFound 表示查询无结果（One 无匹配）。包装标准库 sql.ErrNoRows。
// errors.Is(err, ErrNotFound) 与 errors.Is(err, sql.ErrNoRows) 均兼容。
var ErrNotFound = query.ErrNotFound

// ErrNoRows 是 ErrNotFound 的别名（兼容习惯）。
var ErrNoRows = ErrNotFound
