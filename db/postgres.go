package db

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type PostgresSQL struct {
	pg *sql.DB
}

func ConnectPostgresSQl() (*sql.DB, error) {
	fmt.Println("opening postgres connection")

	dsn := "host=localhost port=5432 user=postgres password=yeshu2004 dbname=tupledb sslmode=disable"
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(time.Minute * 5)

	if err := db.Ping(); err != nil {
		return nil, err
	}

	return db, nil
}