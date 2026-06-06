package db

import (
	"database/sql"
)

type PostgresSQL struct{
	pg *sql.DB
}

func ConnectPostgresSQl() (*sql.DB, error){
	dsn := "user=postgres dbname=tupledb sslmode=verify-full"
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}

	return db, nil;
}