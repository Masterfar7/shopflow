package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func main() {
	dirFlag := flag.String("dir", "./migrations", "directory with migration files")
	dsnFlag := flag.String("dsn", "", "PostgreSQL database connection string")
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		fmt.Println("Usage: migrate -dir <dir> -dsn <dsn> <up|down|status|version|reset>")
		os.Exit(1)
	}

	command := args[0]
	cmdArgs := args[1:]

	if *dsnFlag == "" {
		*dsnFlag = os.Getenv("DATABASE_URL")
		if *dsnFlag == "" {
			log.Fatal("database connection string (-dsn or DATABASE_URL env) must be provided")
		}
	}

	if err := goose.SetDialect("postgres"); err != nil {
		log.Fatalf("goose set dialect error: %v", err)
	}

	db, err := sql.Open("pgx", *dsnFlag)
	if err != nil {
		log.Fatalf("failed to connect to database: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if err := goose.RunContext(ctx, command, db, *dirFlag, cmdArgs...); err != nil {
		log.Fatalf("goose run %s error: %v", command, err)
	}
}
