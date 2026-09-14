package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5/pgxpool"
)

type User struct {
	ID        int64
	Name      string
	Email     string
	CreatedAt time.Time
}

func main() {
	// 启动内嵌PG
	postgres := embeddedpostgres.NewDatabase(
		embeddedpostgres.DefaultConfig().
			Locale("C").
			StartParameters(map[string]string{"lc_messages": "C"}).
			Logger(io.Discard),
	)
	if err := postgres.Start(); err != nil {
		log.Fatal(err)
	}
	defer postgres.Stop()

	connStr := "postgres://postgres:postgres@127.0.0.1:5432/postgres?sslmode=disable"
	ctx := context.Background()

	pool, err := newPool(ctx, connStr)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		log.Fatal(err)
	}
	fmt.Println("Successfully connected to PostgreSQL!")

	if err := createTable(ctx, pool); err != nil {
		log.Fatal(err)
	}

	// Create
	id, err := createUser(ctx, pool, "Alice", "alice@example.com")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("插入成功, ID =", id)

	id2, _ := createUser(ctx, pool, "Bob", "bob@example.com")
	fmt.Println("插入成功, ID =", id2)

	// Read - 单条
	u, err := getUser(ctx, pool, id)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("查询到: %+v\n", u)

	// Read - 列表
	users, err := listUsers(ctx, pool)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("全部用户:")
	for _, u := range users {
		fmt.Printf("  %+v\n", u)
	}

	// Update
	if err := updateUser(ctx, pool, id, "Alice Wang", "alice.wang@example.com"); err != nil {
		log.Fatal(err)
	}
	fmt.Println("更新成功")

	updated, _ := getUser(ctx, pool, id)
	fmt.Printf("更新后: %+v\n", updated)

	// Delete
	if err := deleteUser(ctx, pool, id2); err != nil {
		log.Fatal(err)
	}
	fmt.Println("删除成功, ID =", id2)

	remaining, _ := listUsers(ctx, pool)
	fmt.Println("删除后剩余用户:")
	for _, u := range remaining {
		fmt.Printf("  %+v\n", u)
	}

	// 事务示例：批量创建，任意一步失败则全部回滚
	if err := createUsersInTx(ctx, pool, []User{
		{Name: "Carol", Email: "carol@example.com"},
		{Name: "Dave", Email: "dave@example.com"},
	}); err != nil {
		log.Fatal(err)
	}
	fmt.Println("事务批量插入成功")

	final, _ := listUsers(ctx, pool)
	fmt.Println("最终用户列表:")
	for _, u := range final {
		fmt.Printf("  %+v\n", u)
	}
}

// ---------- 连接池初始化 ----------

func newPool(ctx context.Context, connStr string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(connStr)
	if err != nil {
		return nil, err
	}

	// 按需调整这些参数
	cfg.MaxConns = 10                      // 最大连接数
	cfg.MinConns = 2                       // 最小空闲连接数
	cfg.MaxConnLifetime = time.Hour        // 单个连接最长存活时间
	cfg.MaxConnIdleTime = 30 * time.Minute // 空闲连接多久后被回收
	cfg.HealthCheckPeriod = time.Minute    // 健康检查间隔

	return pgxpool.NewWithConfig(ctx, cfg)
}

// ---------- CRUD 函数（改用 *pgxpool.Pool） ----------

func createTable(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS users (
			id         BIGSERIAL PRIMARY KEY,
			name       TEXT NOT NULL,
			email      TEXT NOT NULL UNIQUE,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`)
	return err
}

func createUser(ctx context.Context, pool *pgxpool.Pool, name, email string) (int64, error) {
	var id int64
	err := pool.QueryRow(ctx,
		`INSERT INTO users (name, email) VALUES ($1, $2) RETURNING id`,
		name, email,
	).Scan(&id)
	return id, err
}
func listUsers(ctx context.Context, pool *pgxpool.Pool) ([]User, error) {
	var users []User
	err := pgxscan.Select(ctx, pool, &users,
		`SELECT id, name, email, created_at FROM users ORDER BY id`,
	)
	return users, err
}

func getUser(ctx context.Context, pool *pgxpool.Pool, id int64) (*User, error) {
	var u User
	err := pgxscan.Get(ctx, pool, &u,
		`SELECT id, name, email, created_at FROM users WHERE id = $1`,
		id,
	)
	if err != nil {
		return nil, err
	}
	return &u, nil
}
func updateUser(ctx context.Context, pool *pgxpool.Pool, id int64, name, email string) error {
	tag, err := pool.Exec(ctx,
		`UPDATE users SET name = $1, email = $2 WHERE id = $3`,
		name, email, id,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("用户 %d 不存在，未更新", id)
	}
	return nil
}

func deleteUser(ctx context.Context, pool *pgxpool.Pool, id int64) error {
	tag, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("用户 %d 不存在，未删除", id)
	}
	return nil
}

// ---------- 事务示例 ----------

func createUsersInTx(ctx context.Context, pool *pgxpool.Pool, users []User) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	// 如果最终没有Commit，defer的Rollback会自动生效（Commit后Rollback是空操作，不会报错）
	defer tx.Rollback(ctx)

	for _, u := range users {
		_, err := tx.Exec(ctx,
			`INSERT INTO users (name, email) VALUES ($1, $2)`,
			u.Name, u.Email,
		)
		if err != nil {
			return fmt.Errorf("插入 %s 失败, 事务将回滚: %w", u.Name, err)
		}
	}

	return tx.Commit(ctx)
}
