package col

import (
	"database/sql/driver"
	"time"

	"github.com/sth4me/fusion/meta"
)

// SoftDelete 是软删除字段类型，对应数据库的 deleted_at 列（TIMESTAMP，NULL=未删除）。
//
// 用法：在模型里声明一个 SoftDelete 字段，fusion 会自动：
//   - 查询时追加 WHERE deleted_at IS NULL（除非 Unscoped()）
//   - Delete 改写为 UPDATE SET deleted_at = now()（而非物理 DELETE）
//
// 列名固定为 deleted_at（符合你的架构约定）。这是"字段即描述符"模式的体现：
// SoftDelete 类型自身表明"我是软删除列"，无需 tag。
//
// 零值（未 Set）= NULL = 未删除；Set(now) = 已删除。
type SoftDelete struct {
	val   *time.Time
	set   bool
	col   string // 列名（由 meta.Register 经 SetMeta 填充，默认 deleted_at）
	table string // 表名
}

// Set 标记为已删除并设置删除时间。
func (s *SoftDelete) Set(t time.Time) {
	s.val = &t
	s.set = true
}

// Get 返回删除时间（未删除返回 nil）。
func (s *SoftDelete) Get() *time.Time { return s.val }

// IsSet 报告是否被显式赋值（用于 DML 的 dirty 判断）。
func (s *SoftDelete) IsSet() bool { return s.set }

// SQLValue 返回 SQL 值（供 DML 参数用）。
func (s *SoftDelete) SQLValue() (any, error) { return s.Value() }

// ColName 返回列名（由 meta.Register 填充）。
func (s *SoftDelete) ColName() string { return s.col }

// SetMeta 由 meta.Register 反射调用，填充列名（实现 FieldDescriptor）。
// 列名固定为 deleted_at（忽略 FieldMeta.Column，因为软删除列名是约定）。
func (s *SoftDelete) SetMeta(m meta.FieldMeta) {
	s.col = "deleted_at"
	s.table = m.Table
}

// Value 实现 driver.Valuer。
func (s SoftDelete) Value() (driver.Value, error) {
	if s.val == nil {
		return nil, nil
	}
	return *s.val, nil
}

// Scan 实现 sql.Scanner（读回 deleted_at 值）。
func (s *SoftDelete) Scan(src any) error {
	if src == nil {
		s.val = nil
		return nil
	}
	switch x := src.(type) {
	case time.Time:
		s.val = &x
		return nil
	case []byte:
		t, err := time.Parse("2006-01-02 15:04:05.999999999 -0700 MST", string(x))
		if err != nil {
			// 尝试其他常见格式
			for _, layout := range []string{
				"2006-01-02 15:04:05.999999999 -0700 MST",
				"2006-01-02 15:04:05 -0700 MST",
				time.RFC3339Nano, time.RFC3339,
				"2006-01-02 15:04:05", "2006-01-02",
			} {
				if t, err = time.Parse(layout, string(x)); err == nil {
					s.val = &t
					return nil
				}
			}
			return err
		}
		s.val = &t
		return nil
	case string:
		t, err := time.Parse(time.RFC3339Nano, x)
		if err != nil {
			for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
				if t, err = time.Parse(layout, x); err == nil {
					s.val = &t
					return nil
				}
			}
			return err
		}
		s.val = &t
		return nil
	}
	return nil
}

// IsDeleted 报告是否已软删除（deleted_at 非 NULL）。
func (s *SoftDelete) IsDeleted() bool { return s.val != nil }
