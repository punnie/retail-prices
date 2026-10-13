package rawlog

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
)

// Product represents a product with its price information
type Product struct {
	ID          int64     `json:"id"`
	Store       string    `json:"store"`
	Name        string    `json:"name"`
	Price       float64   `json:"price"`
	Currency    string    `json:"currency"`
	URL         string    `json:"url"`
	ImageURL    string    `json:"image_url,omitempty"`
	Category    string    `json:"category,omitempty"`
	Brand       string    `json:"brand,omitempty"`
	Description string    `json:"description,omitempty"`
	ScrapedAt   time.Time `json:"scraped_at"`
}

// Storage interface defines the methods for storing and retrieving product data
type Storage interface {
	SaveProduct(product *Product) error
	GetProductsByStore(store string) ([]*Product, error)
	GetProductsByName(name string) ([]*Product, error)
	GetPriceHistory(store, name string) ([]*Product, error)
	Close() error
}

// SQLiteStorage implements Storage using SQLite
type SQLiteStorage struct {
	db *sql.DB
}

// NewSQLiteStorage creates a new SQLite storage instance
func NewSQLiteStorage(dbPath string) (*SQLiteStorage, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, err
	}

	storage := &SQLiteStorage{db: db}
	if err := storage.createTables(); err != nil {
		return nil, err
	}

	return storage, nil
}

// createTables creates the necessary database tables
func (s *SQLiteStorage) createTables() error {
	query := `
	CREATE TABLE IF NOT EXISTS products (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		store TEXT NOT NULL,
		name TEXT NOT NULL,
		price REAL NOT NULL,
		currency TEXT NOT NULL DEFAULT 'EUR',
		url TEXT,
		image_url TEXT,
		category TEXT,
		brand TEXT,
		description TEXT,
		scraped_at DATETIME NOT NULL,
		UNIQUE(store, name, price, scraped_at)
	);

	CREATE INDEX IF NOT EXISTS idx_store ON products(store);
	CREATE INDEX IF NOT EXISTS idx_name ON products(name);
	CREATE INDEX IF NOT EXISTS idx_scraped_at ON products(scraped_at);
	CREATE INDEX IF NOT EXISTS idx_store_name ON products(store, name);
	`

	_, err := s.db.Exec(query)
	return err
}

// SaveProduct saves a product to the database
func (s *SQLiteStorage) SaveProduct(product *Product) error {
	query := `
	INSERT OR IGNORE INTO products 
	(store, name, price, currency, url, image_url, category, brand, description, scraped_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err := s.db.Exec(query,
		product.Store,
		product.Name,
		product.Price,
		product.Currency,
		product.URL,
		product.ImageURL,
		product.Category,
		product.Brand,
		product.Description,
		product.ScrapedAt,
	)

	return err
}

// GetProductsByStore retrieves all products from a specific store
func (s *SQLiteStorage) GetProductsByStore(store string) ([]*Product, error) {
	query := `
	SELECT id, store, name, price, currency, url, image_url, category, brand, description, scraped_at
	FROM products 
	WHERE store = ?
	ORDER BY scraped_at DESC
	`

	rows, err := s.db.Query(query, store)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.scanProducts(rows)
}

// GetProductsByName retrieves all products with a specific name across all stores
func (s *SQLiteStorage) GetProductsByName(name string) ([]*Product, error) {
	query := `
	SELECT id, store, name, price, currency, url, image_url, category, brand, description, scraped_at
	FROM products 
	WHERE name LIKE ?
	ORDER BY scraped_at DESC
	`

	rows, err := s.db.Query(query, "%"+name+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.scanProducts(rows)
}

// GetPriceHistory retrieves price history for a specific product in a specific store
func (s *SQLiteStorage) GetPriceHistory(store, name string) ([]*Product, error) {
	query := `
	SELECT id, store, name, price, currency, url, image_url, category, brand, description, scraped_at
	FROM products 
	WHERE store = ? AND name LIKE ?
	ORDER BY scraped_at ASC
	`

	rows, err := s.db.Query(query, store, "%"+name+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.scanProducts(rows)
}

// scanProducts scans database rows into Product structs
func (s *SQLiteStorage) scanProducts(rows *sql.Rows) ([]*Product, error) {
	var products []*Product

	for rows.Next() {
		product := &Product{}
		var imageURL, category, brand, description sql.NullString

		err := rows.Scan(
			&product.ID,
			&product.Store,
			&product.Name,
			&product.Price,
			&product.Currency,
			&product.URL,
			&imageURL,
			&category,
			&brand,
			&description,
			&product.ScrapedAt,
		)
		if err != nil {
			return nil, err
		}

		if imageURL.Valid {
			product.ImageURL = imageURL.String
		}
		if category.Valid {
			product.Category = category.String
		}
		if brand.Valid {
			product.Brand = brand.String
		}
		if description.Valid {
			product.Description = description.String
		}

		products = append(products, product)
	}

	return products, rows.Err()
}

// Close closes the database connection
func (s *SQLiteStorage) Close() error {
	return s.db.Close()
}

// PostgreSQLStorage implements Storage using PostgreSQL
type PostgreSQLStorage struct {
	db *sql.DB
}

// NewPostgreSQLStorage creates a new PostgreSQL storage instance
func NewPostgreSQLStorage(connectionString string) (*PostgreSQLStorage, error) {
	db, err := sql.Open("postgres", connectionString)
	if err != nil {
		return nil, err
	}

	// Test the connection
	if err := db.Ping(); err != nil {
		return nil, err
	}

	storage := &PostgreSQLStorage{db: db}
	if err := storage.createTables(); err != nil {
		return nil, err
	}

	return storage, nil
}

// createTables creates the necessary database tables for PostgreSQL
func (p *PostgreSQLStorage) createTables() error {
	query := `
	CREATE TABLE IF NOT EXISTS products (
		id SERIAL PRIMARY KEY,
		store VARCHAR(255) NOT NULL,
		name TEXT NOT NULL,
		price DECIMAL(10,2) NOT NULL,
		currency VARCHAR(10) NOT NULL DEFAULT 'EUR',
		url TEXT,
		image_url TEXT,
		category VARCHAR(255),
		brand VARCHAR(255),
		description TEXT,
		scraped_at TIMESTAMP NOT NULL,
		UNIQUE(store, name, price, scraped_at)
	);

	CREATE INDEX IF NOT EXISTS idx_products_store ON products(store);
	CREATE INDEX IF NOT EXISTS idx_products_name ON products(name);
	CREATE INDEX IF NOT EXISTS idx_products_scraped_at ON products(scraped_at);
	CREATE INDEX IF NOT EXISTS idx_products_store_name ON products(store, name);
	`

	_, err := p.db.Exec(query)
	return err
}

// SaveProduct saves a product to the PostgreSQL database
func (p *PostgreSQLStorage) SaveProduct(product *Product) error {
	query := `
	INSERT INTO products 
	(store, name, price, currency, url, image_url, category, brand, description, scraped_at)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	ON CONFLICT (store, name, price, scraped_at) DO NOTHING
	`

	_, err := p.db.Exec(query,
		product.Store,
		product.Name,
		product.Price,
		product.Currency,
		product.URL,
		product.ImageURL,
		product.Category,
		product.Brand,
		product.Description,
		product.ScrapedAt,
	)

	return err
}

// GetProductsByStore retrieves all products from a specific store
func (p *PostgreSQLStorage) GetProductsByStore(store string) ([]*Product, error) {
	query := `
	SELECT id, store, name, price, currency, url, image_url, category, brand, description, scraped_at
	FROM products 
	WHERE store = $1
	ORDER BY scraped_at DESC
	`

	rows, err := p.db.Query(query, store)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return p.scanProducts(rows)
}

// GetProductsByName retrieves all products with a specific name across all stores
func (p *PostgreSQLStorage) GetProductsByName(name string) ([]*Product, error) {
	query := `
	SELECT id, store, name, price, currency, url, image_url, category, brand, description, scraped_at
	FROM products 
	WHERE name ILIKE $1
	ORDER BY scraped_at DESC
	`

	rows, err := p.db.Query(query, "%"+name+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return p.scanProducts(rows)
}

// GetPriceHistory retrieves price history for a specific product in a specific store
func (p *PostgreSQLStorage) GetPriceHistory(store, name string) ([]*Product, error) {
	query := `
	SELECT id, store, name, price, currency, url, image_url, category, brand, description, scraped_at
	FROM products 
	WHERE store = $1 AND name ILIKE $2
	ORDER BY scraped_at ASC
	`

	rows, err := p.db.Query(query, store, "%"+name+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return p.scanProducts(rows)
}

// scanProducts scans database rows into Product structs for PostgreSQL
func (p *PostgreSQLStorage) scanProducts(rows *sql.Rows) ([]*Product, error) {
	var products []*Product

	for rows.Next() {
		product := &Product{}
		var imageURL, category, brand, description sql.NullString

		err := rows.Scan(
			&product.ID,
			&product.Store,
			&product.Name,
			&product.Price,
			&product.Currency,
			&product.URL,
			&imageURL,
			&category,
			&brand,
			&description,
			&product.ScrapedAt,
		)
		if err != nil {
			return nil, err
		}

		if imageURL.Valid {
			product.ImageURL = imageURL.String
		}
		if category.Valid {
			product.Category = category.String
		}
		if brand.Valid {
			product.Brand = brand.String
		}
		if description.Valid {
			product.Description = description.String
		}

		products = append(products, product)
	}

	return products, rows.Err()
}

// Close closes the PostgreSQL database connection
func (p *PostgreSQLStorage) Close() error {
	return p.db.Close()
}

// NewStorage creates a new storage instance based on the database type
// For SQLite: connectionString should be the file path
// For PostgreSQL: connectionString should be a PostgreSQL connection string
func NewStorage(dbType, connectionString string) (Storage, error) {
	switch dbType {
	case "sqlite", "sqlite3":
		return NewSQLiteStorage(connectionString)
	case "postgres", "postgresql":
		return NewPostgreSQLStorage(connectionString)
	default:
		return nil, fmt.Errorf("unsupported database type: %s", dbType)
	}
}
