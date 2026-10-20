package db_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/stjosephsplacements/AetherCode-Execution-Engine/internal/db"
	"github.com/stjosephsplacements/AetherCode-Execution-Engine/internal/model"
	"github.com/stjosephsplacements/AetherCode-Execution-Engine/migrations"

	"github.com/jackc/pgx/v5/pgxpool"
)

// sharedPool is set in TestMain and reused by all subtests.
var sharedPool *pgxpool.Pool

// sharedDSN is the DSN of the shared test container, set in TestMain.
var sharedDSN string

func TestMain(m *testing.M) {
	ctx := context.Background()

	pgc, err := tcpostgres.Run(ctx,
		"postgres:18",
		tcpostgres.WithDatabase("aethercode_test"),
		tcpostgres.WithUsername("aethercode"),
		tcpostgres.WithPassword("aethercode"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		// Docker unavailable or image pull failed — skip gracefully.
		if strings.Contains(err.Error(), "Cannot connect") ||
			strings.Contains(err.Error(), "docker") ||
			strings.Contains(err.Error(), "no such host") {
			os.Exit(0) // all tests skipped
		}
		// Unexpected error — fail loudly.
		panic("testcontainers: " + err.Error())
	}
	defer pgc.Terminate(ctx) //nolint:errcheck

	dsn, err := pgc.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		panic("testcontainers dsn: " + err.Error())
	}

	sharedDSN = dsn

	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		panic("db.Connect: " + err.Error())
	}
	if err := db.Migrate(ctx, pool, migrations.FS); err != nil {
		panic("db.Migrate: " + err.Error())
	}
	sharedPool = pool

	code := m.Run()
	pool.Close()
	os.Exit(code)
}

// ── helpers ────────────────────────────────────────────────────────────────

func newSub(mode string) *model.Submission {
	return &model.Submission{
		ID:         uuid.New(),
		TenantID:   model.DefaultTenantID,
		Mode:       mode,
		Language:   "python",
		SourceCode: "print('hello')",
		Status:     model.StatusQueued,
	}
}

func mustInsert(t *testing.T, sub *model.Submission) {
	t.Helper()
	if err := db.InsertSubmission(context.Background(), sharedPool, sub); err != nil {
		t.Fatalf("InsertSubmission: %v", err)
	}
}

// freshDB creates an empty database on the shared container and returns a pool
// connected to it. The pool is closed on cleanup.
func freshDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("rand: %v", err)
	}
	name := "m_" + hex.EncodeToString(buf)
	if _, err := sharedPool.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	cfg, err := pgxpool.ParseConfig(sharedDSN)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect fresh db: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// migrationsUpTo returns the embedded migrations whose numeric prefix is <= version.
func migrationsUpTo(t *testing.T, version int) fs.FS {
	t.Helper()
	out := fstest.MapFS{}
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		prefix, _, ok := strings.Cut(name, "_")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(prefix)
		if err != nil || n > version {
			continue
		}
		data, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		out[name] = &fstest.MapFile{Data: data}
	}
	return out
}

// ── tests ──────────────────────────────────────────────────────────────────

func TestTruncate(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"hello", 10, "hello"},
		{"hello", 3, "hel"},
		{"", 5, ""},
		// 3-byte UTF-8 rune — should not split mid-rune
		{"日本語", 4, "日"},
	}
	for _, tc := range cases {
		got := db.Truncate(tc.in, tc.max)
		if got != tc.want {
			t.Errorf("Truncate(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
		}
	}
}

func TestInsertGetSubmission(t *testing.T) {
	ctx := context.Background()
	sub := newSub("run")
	mustInsert(t, sub)

	got, err := db.GetSubmission(ctx, sharedPool, sub.ID)
	if err != nil {
		t.Fatalf("GetSubmission: %v", err)
	}
	if got.ID != sub.ID {
		t.Errorf("ID mismatch: got %v want %v", got.ID, sub.ID)
	}
	if got.Language != "python" {
		t.Errorf("Language = %q, want python", got.Language)
	}
	if got.Status != model.StatusQueued {
		t.Errorf("Status = %q, want queued", got.Status)
	}
	if got.Mode != "run" {
		t.Errorf("Mode = %q, want run", got.Mode)
	}
}

func TestUpdateSubmissionStatus(t *testing.T) {
	ctx := context.Background()
	sub := newSub("run")
	mustInsert(t, sub)

	// First transition: queued → running (should succeed).
	if err := db.UpdateSubmissionStatus(ctx, sharedPool, sub.ID, model.StatusRunning); err != nil {
		t.Fatalf("first status update: %v", err)
	}

	// Move to completed so it becomes terminal.
	if _, err := sharedPool.Exec(ctx,
		`UPDATE submissions SET status='completed' WHERE id=$1`, sub.ID); err != nil {
		t.Fatalf("force completed: %v", err)
	}

	// Attempt to update a terminal submission — must return the "already terminal" sentinel.
	err := db.UpdateSubmissionStatus(ctx, sharedPool, sub.ID, model.StatusRunning)
	if err == nil || !strings.Contains(err.Error(), "already terminal") {
		t.Errorf("expected 'already terminal' error, got: %v", err)
	}
}

func TestInsertSubmissionIdempotent(t *testing.T) {
	ctx := context.Background()
	userID := uuid.New()

	// Insert user so FK is satisfied.
	if _, err := sharedPool.Exec(ctx,
		`INSERT INTO users (id, tenant_id, external_subject_id) VALUES ($1,$2,'test-sub-idem')`,
		userID, model.DefaultTenantID); err != nil {
		t.Fatalf("insert user: %v", err)
	}

	key := "idem-key-" + uuid.New().String()
	sub := newSub("submit")
	sub.UserID = &userID
	ikey := key
	sub.IdempotencyKey = &ikey

	id1, isNew1, err := db.InsertSubmissionIdempotent(ctx, sharedPool, sub)
	if err != nil {
		t.Fatalf("first idempotent insert: %v", err)
	}
	if !isNew1 {
		t.Error("first call: expected isNew=true")
	}

	sub2 := newSub("run") // different mode: key is no longer scoped by mode
	sub2.ID = uuid.New()
	sub2.UserID = &userID
	sub2.IdempotencyKey = &ikey

	id2, isNew2, err := db.InsertSubmissionIdempotent(ctx, sharedPool, sub2)
	if err != nil {
		t.Fatalf("second idempotent insert: %v", err)
	}
	if isNew2 {
		t.Error("second call: expected isNew=false")
	}
	if id1 != id2 {
		t.Errorf("expected same ID: %v != %v", id1, id2)
	}
}

func TestInsertResultsAndVerdict(t *testing.T) {
	ctx := context.Background()
	sub := newSub("run")
	mustInsert(t, sub)

	results := []model.TestResult{
		{TestIndex: 0, Verdict: model.VerdictAccepted, CPUTimeNs: 100000, MemoryBytes: 1024, StdoutPreview: "hello", StderrPreview: ""},
		{TestIndex: 1, Verdict: model.VerdictWrongAnswer, CPUTimeNs: 200000, MemoryBytes: 2048},
	}

	err := db.InsertResultsAndVerdict(ctx, sharedPool, sub.ID, results,
		model.StatusCompleted, model.VerdictWrongAnswer, "",
		300000, 2048, 2, 1)
	if err != nil {
		t.Fatalf("InsertResultsAndVerdict: %v", err)
	}

	got, err := db.GetSubmission(ctx, sharedPool, sub.ID)
	if err != nil {
		t.Fatalf("GetSubmission after verdict: %v", err)
	}
	if got.Status != model.StatusCompleted {
		t.Errorf("Status = %q, want completed", got.Status)
	}
	if got.Verdict != model.VerdictWrongAnswer {
		t.Errorf("Verdict = %q, want wrong_answer", got.Verdict)
	}
	if got.TestCount != 2 {
		t.Errorf("TestCount = %d, want 2", got.TestCount)
	}
	if got.TestsPassed != 1 {
		t.Errorf("TestsPassed = %d, want 1", got.TestsPassed)
	}

	// Second call must return "verdict already written".
	err2 := db.InsertResultsAndVerdict(ctx, sharedPool, sub.ID, results,
		model.StatusCompleted, model.VerdictWrongAnswer, "",
		300000, 2048, 2, 1)
	if err2 == nil || !strings.Contains(err2.Error(), "verdict already written") {
		t.Errorf("expected 'verdict already written', got: %v", err2)
	}
}

func TestOrphanedSubmissions(t *testing.T) {
	ctx := context.Background()

	// A fresh queued submission — should NOT appear (updated_at = now()).
	fresh := newSub("run")
	mustInsert(t, fresh)

	// An old queued submission — should appear (updated_at set to 3 min ago).
	old := newSub("submit")
	mustInsert(t, old)
	if _, err := sharedPool.Exec(ctx,
		`UPDATE submissions SET updated_at = now() - interval '3 minutes' WHERE id = $1`, old.ID); err != nil {
		t.Fatalf("backdate updated_at: %v", err)
	}

	orphans, err := db.OrphanedSubmissions(ctx, sharedPool)
	if err != nil {
		t.Fatalf("OrphanedSubmissions: %v", err)
	}

	found := false
	for _, o := range orphans {
		if o.ID == old.ID {
			found = true
			if o.Mode != "submit" {
				t.Errorf("orphan Mode = %q, want submit", o.Mode)
			}
		}
		if o.ID == fresh.ID {
			t.Errorf("fresh submission should not appear in orphans")
		}
	}
	if !found {
		t.Errorf("old submission not found in orphans")
	}
}

func TestGetProblemVersion(t *testing.T) {
	ctx := context.Background()

	probID := uuid.New()
	pvID := uuid.New()
	cpuNs := int64(500_000_000)
	memB := int64(268_435_456)

	if _, err := sharedPool.Exec(ctx,
		`INSERT INTO problems (id, tenant_id, title, slug) VALUES ($1,$2,'Test',$3)`,
		probID, model.DefaultTenantID, "test-pv-"+probID.String()); err != nil {
		t.Fatalf("insert problem: %v", err)
	}
	if _, err := sharedPool.Exec(ctx,
		`INSERT INTO problem_versions (id, problem_id, tenant_id, version, cpu_limit_ns, memory_limit, checker_type)
		 VALUES ($1,$2,$3,1,$4,$5,'exact')`,
		pvID, probID, model.DefaultTenantID, cpuNs, memB); err != nil {
		t.Fatalf("insert problem_version: %v", err)
	}

	pv, err := db.GetProblemVersion(ctx, sharedPool, pvID)
	if err != nil {
		t.Fatalf("GetProblemVersion: %v", err)
	}
	if pv.ID != pvID {
		t.Errorf("ID = %v, want %v", pv.ID, pvID)
	}
	if pv.CPULimitNs == nil || *pv.CPULimitNs != cpuNs {
		t.Errorf("CPULimitNs = %v, want %d", pv.CPULimitNs, cpuNs)
	}
	if pv.MemoryLimit == nil || *pv.MemoryLimit != memB {
		t.Errorf("MemoryLimit = %v, want %d", pv.MemoryLimit, memB)
	}
	if pv.CheckerType != "exact" {
		t.Errorf("CheckerType = %q, want exact", pv.CheckerType)
	}
}

func TestGetTestCases(t *testing.T) {
	ctx := context.Background()

	probID := uuid.New()
	pvID := uuid.New()

	if _, err := sharedPool.Exec(ctx,
		`INSERT INTO problems (id, tenant_id, title, slug) VALUES ($1,$2,'TC Test',$3)`,
		probID, model.DefaultTenantID, "tc-test-"+probID.String()); err != nil {
		t.Fatalf("insert problem: %v", err)
	}
	if _, err := sharedPool.Exec(ctx,
		`INSERT INTO problem_versions (id, problem_id, tenant_id, version) VALUES ($1,$2,$3,1)`,
		pvID, probID, model.DefaultTenantID); err != nil {
		t.Fatalf("insert problem_version: %v", err)
	}

	// Insert out-of-ordinal order to verify ORDER BY ordinal.
	for _, tc := range []struct {
		ord      int
		input    string
		expected string
		sample   bool
	}{
		{2, "b", "B", false},
		{0, "a", "A", true},
		{1, "c", "C", false},
	} {
		if _, err := sharedPool.Exec(ctx,
			`INSERT INTO test_cases (problem_version_id, tenant_id, ordinal, input, expected_output, is_sample)
			 VALUES ($1,$2,$3,$4,$5,$6)`,
			pvID, model.DefaultTenantID, tc.ord, tc.input, tc.expected, tc.sample); err != nil {
			t.Fatalf("insert test_case ord=%d: %v", tc.ord, err)
		}
	}

	tcs, err := db.GetTestCases(ctx, sharedPool, pvID)
	if err != nil {
		t.Fatalf("GetTestCases: %v", err)
	}
	if len(tcs) != 3 {
		t.Fatalf("len(tcs) = %d, want 3", len(tcs))
	}
	// Must be sorted by ordinal: 0,1,2
	wantInputs := []string{"a", "c", "b"}
	for i, tc := range tcs {
		if tc.Input != wantInputs[i] {
			t.Errorf("tcs[%d].Input = %q, want %q", i, tc.Input, wantInputs[i])
		}
	}
	if !tcs[0].IsSample {
		t.Error("tcs[0] (ordinal 0) should be sample")
	}
	if tcs[1].IsSample || tcs[2].IsSample {
		t.Error("tcs[1] and tcs[2] should not be sample")
	}
	_ = time.Now() // satisfy import
}

func TestMigration006_DedupesCrossModeKeys(t *testing.T) {
	ctx := context.Background()
	pool := freshDB(t)

	if err := db.Migrate(ctx, pool, migrationsUpTo(t, 5)); err != nil {
		t.Fatalf("migrate to 5: %v", err)
	}

	userID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO users (id, tenant_id, external_subject_id) VALUES ($1,$2,'mig006')`,
		userID, model.DefaultTenantID); err != nil {
		t.Fatalf("insert user: %v", err)
	}

	olderID, newerID := uuid.New(), uuid.New()
	insert := func(id uuid.UUID, mode, age string) {
		t.Helper()
		_, err := pool.Exec(ctx, `
			INSERT INTO submissions (id, tenant_id, user_id, idempotency_key, mode,
			                         language, source_code, status, created_at, updated_at)
			VALUES ($1, $2, $3, 'k', $4, 'python', 'print(1)', 'queued',
			        now() - $5::interval, now())`,
			id, model.DefaultTenantID, userID, mode, age)
		if err != nil {
			t.Fatalf("insert submission (%s): %v", mode, err)
		}
	}
	insert(olderID, "submit", "1 minute")
	insert(newerID, "run", "0 seconds")

	if err := db.Migrate(ctx, pool, migrationsUpTo(t, 6)); err != nil {
		t.Fatalf("migrate to 6: %v", err)
	}

	var olderKey, newerKey *string
	if err := pool.QueryRow(ctx, `SELECT idempotency_key FROM submissions WHERE id=$1`, olderID).Scan(&olderKey); err != nil {
		t.Fatalf("read older: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT idempotency_key FROM submissions WHERE id=$1`, newerID).Scan(&newerKey); err != nil {
		t.Fatalf("read newer: %v", err)
	}
	if olderKey == nil || *olderKey != "k" {
		t.Errorf("older row key = %v, want \"k\"", olderKey)
	}
	if newerKey != nil {
		t.Errorf("newer row key = %q, want NULL", *newerKey)
	}

	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM pg_indexes WHERE indexname = 'idx_submissions_idempotency_v2'`).Scan(&n); err != nil {
		t.Fatalf("pg_indexes: %v", err)
	}
	if n != 1 {
		t.Errorf("idx_submissions_idempotency_v2 count = %d, want 1", n)
	}
}
