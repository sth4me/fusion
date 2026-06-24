package fusion_test

import "database/sql"

func execInsert(db *sql.DB, q string) {
	if _, err := db.Exec(q); err != nil {
		panic(err)
	}
}
