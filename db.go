package main

import (
	"database/sql"
	_ "github.com/lib/pq"
)

// connectDB подключается к базе данных PostgreSQL
func connectDB() (*sql.DB, error) {
	connStr := "user=postgres dbname=chatdb sslmode=disable password=root"
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return nil, err
	}
	return db, nil
}
