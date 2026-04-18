package api

import (
	"embed"
	"os"
	"testing"

	"github.com/cidekar/adele-framework"
	"github.com/cidekar/adele-framework/database"
	"github.com/joho/godotenv"
	up "github.com/upper/db/v4"
)

// Run from the api package directory:
//
//	go test . -v
//	go test -coverprofile=coverage.out . && go tool cover -html=coverage.out
//	go test . --run TestQueue_Dispatch_Job

//go:embed testmigrations
var testTemplateFS embed.FS

var (
	ade   adele.Adele
	upper up.Session
)

func TestMain(m *testing.M) {
	path, err := os.Getwd()
	if err != nil {
		panic(err)
	}

	ade.RootPath = path + "/testdata"

	// Attempt to load test env; missing file is tolerated so unit tests that
	// do not require a database still run.
	_ = godotenv.Load(ade.RootPath + "/.test.env")

	dbType := os.Getenv("DATABASE_TYPE")
	if dbType != "" {
		dsn := &database.DataSourceName{
			Host:         os.Getenv("DATABASE_HOST"),
			Port:         os.Getenv("DATABASE_PORT"),
			User:         os.Getenv("DATABASE_USER"),
			Password:     os.Getenv("DATABASE_PASSWORD"),
			DatabaseName: os.Getenv("DATABASE_NAME"),
			SslMode:      os.Getenv("DATABASE_SSL_MODE"),
		}
		db, err := database.OpenDB(dbType, dsn)
		if err == nil && db != nil {
			ade.DB = &database.Database{
				DataType: dbType,
				Pool:     db,
			}
			upper = ade.DB.NewSession()
		}
	}

	code := m.Run()

	os.Exit(code)
}

// runMigrations loads the create_jobs_table.postgres.sql file from the
// embedded migrations filesystem and executes it against the shared upper
// session. Tests that need live DB tables should call this at start and
// tearDownDatabase at end.
func runMigrations(t *testing.T) {
	t.Helper()
	if upper == nil {
		t.Skip("skipping test: no database session available (set DATABASE_* env vars in testdata/.test.env)")
	}

	data, err := testTemplateFS.ReadFile("testmigrations/create_jobs_table.postgres.sql")
	if err != nil {
		t.Fatalf("failed to read migration: %v", err)
	}

	if _, err := upper.SQL().Exec(string(data)); err != nil {
		t.Fatalf("failed to execute migration: %v", err)
	}
}

func tearDownDatabase(t *testing.T) {
	t.Helper()
	if upper == nil {
		return
	}
	stmt := "DROP TABLE IF EXISTS jobs CASCADE; DROP TABLE IF EXISTS failed_jobs CASCADE;"
	if _, err := upper.SQL().Exec(stmt); err != nil {
		t.Logf("teardown: %v", err)
	}
}
