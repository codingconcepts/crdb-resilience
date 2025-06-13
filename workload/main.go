package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"time"

	crdbpgx "github.com/cockroachdb/cockroach-go/v2/crdb/crdbpgxv5"
	"github.com/fatih/color"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/samber/lo"
)

const (
	accounts       = 1_000
	initialBalance = float64(10_000)
)

var (
	pink = color.RGB(236, 63, 150).SprintFunc()
	blue = color.RGB(0, 252, 237).SprintFunc()
)

func main() {
	log.SetFlags(0)

	url := flag.String("url", "", "database connection string")
	flag.Parse()

	r := runner{
		accounts: accounts,
	}

	// Configure the connection pool, cleaning up connections after a short time (most
	// probably shorter than you'd apply in production but good for purposes of this demo).
	cfg, err := pgxpool.ParseConfig(*url)
	if err != nil {
		log.Fatalf("error parsing connection string: %v", err)
	}
	cfg.MaxConns = 3

	// These two values combined are what drive the server.shutdown.connections.timeout
	// setting in CockroachDB.
	cfg.MaxConnLifetime = time.Second * 15
	cfg.MaxConnLifetimeJitter = time.Second * 5

	db, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		log.Fatalf("error connecting to database: %v", err)
	}
	defer db.Close()

	if err = db.Ping(context.Background()); err != nil {
		log.Fatalf("error testing database connection: %v", err)
	}

	if err := r.deinit(db); err != nil {
		log.Printf("error destroying database: %v", err)
	}

	if err := r.init(db); err != nil {
		log.Fatalf("error initialising database: %v", err)
	}

	if err := r.run(db); err != nil {
		log.Fatalf("error running simulation: %v", err)
	}
}

type runner struct {
	accounts int

	accountIDs   []string
	selectionIDs []string
}

func (r *runner) init(db *pgxpool.Pool) error {
	if err := initSeed(db, r.accounts, initialBalance); err != nil {
		return fmt.Errorf("seeding database: %w", err)
	}

	accountIDs, err := fetchIDs(db, r.accounts)
	if err != nil {
		return fmt.Errorf("fetching ids: %w", err)
	}

	r.accountIDs = accountIDs
	return nil
}

func (r *runner) deinit(db *pgxpool.Pool) error {
	if err := denit(db); err != nil {
		log.Fatalf("error creating database: %v", err)
	}

	return nil
}

func (r *runner) run(db *pgxpool.Pool) error {
	accountIDs, err := fetchIDs(db, r.accounts)
	if err != nil {
		log.Fatalf("error fetching ids ahead of test: %v", err)
	}
	r.selectionIDs = accountIDs

	var errorCount int
	var totalDowntime time.Duration

	// Perform a transfer every 100ms.
	for range time.NewTicker(time.Millisecond * 100).C {
		ids := lo.Samples(r.selectionIDs, 2)

		taken, err := performTransfer(db, ids[0], ids[1], rand.Intn(100))
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("error: %v", err)
			errorCount++
			totalDowntime += taken
		}

		latencyMS := fmt.Sprintf("%dms", taken.Milliseconds())
		totalDowntimeS := fmt.Sprintf("%0.2fs", totalDowntime.Seconds())

		fmt.Printf(
			"%s -> %s (latency: %s, errors: %s, total downtime: %s)\n",
			ids[0][0:4],
			ids[1][0:4],
			blue(latencyMS),
			pink(errorCount),
			pink(totalDowntimeS),
		)
	}

	panic("unexected app termination")
}

func performTransfer(db *pgxpool.Pool, from, to string, amount int) (elapsed time.Duration, err error) {
	// Timeout queries after 5s (configure to your requirements).
	timeout, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()

	start := time.Now()
	defer func() {
		elapsed = time.Since(start)
	}()

	const stmt = `UPDATE account
									SET balance = CASE 
										WHEN id = $1 THEN balance - $3
										WHEN id = $2 THEN balance + $3
									END
								WHERE id IN ($1, $2);`

	// Wrapping pgx query with crdbpgx to ensure retryable requests are retried
	// for both databases.
	txOptions := pgx.TxOptions{
		IsoLevel: pgx.Serializable,
	}
	err = crdbpgx.ExecuteTx(timeout, db, txOptions, func(tx pgx.Tx) error {
		_, err := db.Exec(timeout, stmt, from, to, amount)
		return err
	})

	return
}

func fetchIDs(db *pgxpool.Pool, count int) ([]string, error) {
	const stmt = `SELECT id FROM account ORDER BY random() LIMIT $1`

	rows, err := db.Query(context.Background(), stmt, count)
	if err != nil {
		return nil, fmt.Errorf("querying for rows: %w", err)
	}

	var accountIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning id: %w", err)
		}
		accountIDs = append(accountIDs, id)
	}

	return accountIDs, nil
}

func initSeed(db *pgxpool.Pool, rowCount int, balance float64) error {
	const stmt = `INSERT INTO account (balance)
								SELECT $2
								FROM generate_series(1, $1)`

	_, err := db.Exec(context.Background(), stmt, rowCount, balance)
	return err
}

func denit(db *pgxpool.Pool) error {
	const stmt = `TRUNCATE account`

	_, err := db.Exec(context.Background(), stmt)
	return err
}
