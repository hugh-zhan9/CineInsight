//go:build darwin || linux

package services

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm/logger"
	"path/filepath"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"video-master/internal/dbtest"
)

func TestBTCredentialManagersCannotCleanPendingPublication(t *testing.T) {
	store := btMemorySecrets{}
	lock := BTFileCredentialLock{Path: filepath.Join(t.TempDir(), "scope.lock")}
	a, b := NewBTCredentialManager(store, lock), NewBTCredentialManager(store, lock)
	publishing, proceed := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	var liveRef string
	go func() {
		_, err := a.Save(context.Background(), "", BTSecretChange{Operation: "replace", Value: "key"}, BTSecretChange{}, func(ref string) (bool, error) { liveRef = ref; close(publishing); <-proceed; return true, nil })
		done <- err
	}()
	<-publishing
	queried := false
	err := b.Cleanup(func() (map[string]bool, error) { queried = true; return map[string]bool{}, nil })
	if !errors.Is(err, ErrBTCredentialsBusy) || queried {
		close(proceed)
		<-done
		t.Fatalf("cleanup crossed publication: %v queried=%v", err, queried)
	}
	if _, err := b.Load(liveRef); !errors.Is(err, ErrBTCredentialsBusy) {
		close(proceed)
		<-done
		t.Fatal("Load skipped namespace lock", err)
	}
	close(proceed)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := b.Cleanup(func() (map[string]bool, error) { return map[string]bool{liveRef: true}, nil }); err != nil {
		t.Fatal(err)
	}
	if got, err := b.Load(liveRef); err != nil || got.ProwlarrKey != "key" {
		t.Fatal("published secret disappeared", err)
	}
	if _, err := os.Stat(lock.Path); err != nil {
		t.Fatal("lock inode removed", err)
	}
}
func TestBTCredentialScopesDoNotDeleteEachOther(t *testing.T) {
	aStore, bStore := btMemorySecrets{}, btMemorySecrets{}
	dir := t.TempDir()
	a := NewBTCredentialManager(aStore, BTFileCredentialLock{Path: filepath.Join(dir, "a.lock")})
	b := NewBTCredentialManager(bStore, BTFileCredentialLock{Path: filepath.Join(dir, "b.lock")})
	ref, err := a.Save(context.Background(), "", BTSecretChange{Operation: "replace", Value: "key-a"}, BTSecretChange{}, func(string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := bStore.Put(ref, []byte(`{"ProwlarrKey":"key-b"}`)); err != nil {
		t.Fatal(err)
	}
	if err := b.Cleanup(func() (map[string]bool, error) { return map[string]bool{}, nil }); err != nil {
		t.Fatal(err)
	}
	got, err := a.Load(ref)
	if err != nil || got.ProwlarrKey != "key-a" {
		t.Fatal("other scope deleted committed credential", err)
	}
}
func TestBTCredentialScopePhysicalDBIdentity(t *testing.T) {
	first, second := dbtest.Open(t), dbtest.Open(t)
	a, err := BTCredentialScope(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	again, err := BTCredentialScope(context.Background(), first)
	if err != nil || a != again || len(a) != 64 {
		t.Fatalf("unstable scope: %s %s %v", a, again, err)
	}
	b, err := BTCredentialScope(context.Background(), second)
	if err != nil || a == b {
		t.Fatalf("independent physical DB conflated: %s %s %v", a, b, err)
	}
	mem, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BTCredentialScope(context.Background(), mem); !errors.Is(err, ErrBTCredentials) {
		t.Fatal("unidentifiable memory database accepted", err)
	}
}
func TestBTCredentialMissingLockerIsRejected(t *testing.T) {
	store := btMemorySecrets{}
	manager := NewBTCredentialManager(store, nil)
	if _, err := manager.Load(""); !errors.Is(err, ErrBTCredentials) {
		t.Fatal("missing lock silently accepted", err)
	}
}

func TestBTFileCredentialLockSubprocess(t *testing.T) {
	if path := os.Getenv("CINEINSIGHT_BT_LOCK_CHILD_PATH"); path != "" {
		unlock, err := (BTFileCredentialLock{Path: path}).TryLock()
		if os.Getenv("CINEINSIGHT_BT_LOCK_CHILD_BUSY") == "1" {
			if !errors.Is(err, ErrBTCredentialsBusy) {
				if unlock != nil {
					unlock()
				}
				t.Fatal("child crossed held lock", err)
			}
		} else {
			if err != nil {
				t.Fatal(err)
			}
			unlock()
		}
		return
	}
	lock := BTFileCredentialLock{Path: filepath.Join(t.TempDir(), "process.lock")}
	unlock, err := lock.TryLock()
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		unlock()
		t.Fatal(err)
	}
	run := func(busy string) {
		command := exec.Command(executable, "-test.run=^TestBTFileCredentialLockSubprocess$", "-test.count=1")
		command.Env = append(os.Environ(), "CINEINSIGHT_BT_LOCK_CHILD_PATH="+lock.Path, "CINEINSIGHT_BT_LOCK_CHILD_BUSY="+busy)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("subprocess lock: %v %s", err, output)
		}
	}
	run("1")
	unlock()
	run("0")
}

// The two client ports forward to the same test database, so SQL reports identical
// server/database/schema identity. Scope must nevertheless distinguish the clients.
func TestBTCredentialScopeSeparatesForwardedEndpoints(t *testing.T) {
	if !dbtest.IsPostgres() {
		t.Skip("requires disposable PostgreSQL")
	}
	source := dbtest.Open(t)
	dialector, ok := source.Dialector.(*postgres.Dialector)
	if !ok {
		t.Fatal("expected pgx-backed PostgreSQL")
	}
	configuration, err := pgx.ParseConfig(dialector.DSN)
	if err != nil {
		t.Fatal("test PostgreSQL connection config unavailable")
	}
	target := net.JoinHostPort(configuration.Host, strconv.Itoa(int(configuration.Port)))
	forward := func() net.Listener {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		var workers sync.WaitGroup
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				client, err := listener.Accept()
				if err != nil {
					return
				}
				workers.Add(1)
				go func() {
					defer workers.Done()
					defer client.Close()
					upstream, err := net.DialTimeout("tcp", target, 5*time.Second)
					if err != nil {
						return
					}
					defer upstream.Close()
					copied := make(chan struct{})
					go func() { _, _ = io.Copy(upstream, client); _ = upstream.Close(); close(copied) }()
					_, _ = io.Copy(client, upstream)
					_ = client.Close()
					<-copied
				}()
			}
		}()
		t.Cleanup(func() {
			_ = listener.Close()
			done := make(chan struct{})
			go func() { workers.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("forwarded test connection did not close")
			}
		})
		return listener
	}
	open := func(listener net.Listener) *gorm.DB {
		cfg := configuration.Copy()
		address := listener.Addr().(*net.TCPAddr)
		cfg.Host = address.IP.String()
		cfg.Port = uint16(address.Port)
		// Preserve the test schema; all connection secrets remain only in pgx memory.
		cfg.Fallbacks = nil
		pool := stdlib.OpenDB(*cfg)
		t.Cleanup(func() { _ = pool.Close() })
		result, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		if err != nil {
			t.Fatal("forwarded test database unavailable")
		}
		return result
	}
	aListener, bListener := forward(), forward()
	a, b := open(aListener), open(bListener)
	identity := func(db *gorm.DB) string {
		var result string
		err := db.Raw("SELECT inet_server_addr()::text || ':' || inet_server_port()::text || '/' || current_database() || '/' || current_schema()").Scan(&result).Error
		if err != nil {
			t.Fatal("SQL identity unavailable")
		}
		return result
	}
	if identity(a) != identity(b) {
		t.Fatal("test did not produce identical server-side identities")
	}
	aScope, err := BTCredentialScope(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	bScope, err := BTCredentialScope(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	if aScope == bScope {
		t.Fatal("distinct client ports sharing SQL identity collided")
	}
	again, err := BTCredentialScope(context.Background(), a)
	if err != nil || again != aScope {
		t.Fatal("same client endpoint scope was unstable", err)
	}
}

func TestBTCredentialPostgresScopeRejectsUnknownDriver(t *testing.T) {
	sqliteDB, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "wrong-driver.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := sqliteDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	wrapped, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("test driver wrapper unavailable")
	}
	if _, err := BTCredentialScope(context.Background(), wrapped); !errors.Is(err, ErrBTCredentials) {
		t.Fatal("non-pgx endpoint was accepted", err)
	}
}
