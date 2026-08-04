package database

import (
	"context"
	"errors"
	"sync"

	"gorm.io/gorm"
)

var ErrMaintenance = errors.New("database is in maintenance mode")

type databaseOperationGate struct {
	mu       sync.Mutex
	cond     *sync.Cond
	active   bool
	inFlight int
}

var operationGate = newDatabaseOperationGate()

type maintenanceAccessKey struct{}

// WithMaintenanceAccess is reserved for the restore lifecycle after external
// access has been fenced. It lets that lifecycle persist its own outcome while
// normal GORM calls continue to fail with ErrMaintenance.
func WithMaintenanceAccess(db *gorm.DB) *gorm.DB {
	ctx := context.WithValue(context.Background(), maintenanceAccessKey{}, true)
	return db.WithContext(ctx)
}

func hasMaintenanceAccess(tx *gorm.DB) bool {
	return tx != nil && tx.Statement != nil && tx.Statement.Context != nil && tx.Statement.Context.Value(maintenanceAccessKey{}) == true
}

func newDatabaseOperationGate() *databaseOperationGate {
	gate := &databaseOperationGate{}
	gate.cond = sync.NewCond(&gate.mu)
	return gate
}

func (gate *databaseOperationGate) enter() error {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if gate.active {
		return ErrMaintenance
	}
	gate.inFlight++
	return nil
}

func (gate *databaseOperationGate) leave() {
	gate.mu.Lock()
	gate.inFlight--
	if gate.inFlight == 0 {
		gate.cond.Broadcast()
	}
	gate.mu.Unlock()
}

func (gate *databaseOperationGate) beginMaintenance() func() {
	gate.mu.Lock()
	gate.active = true
	for gate.inFlight > 0 {
		gate.cond.Wait()
	}
	gate.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			gate.mu.Lock()
			gate.active = false
			gate.cond.Broadcast()
			gate.mu.Unlock()
		})
	}
}

// BeginMaintenance rejects new database operations and waits for operations
// already registered with the gate to finish. The returned release function
// must only be called when the existing database is safe to use again.
func BeginMaintenance() func() {
	return operationGate.beginMaintenance()
}

// Transaction keeps the maintenance gate for the full lifetime of an explicit
// GORM transaction. Statement callbacks alone cannot cover the gaps between
// statements in a user-managed transaction.
func Transaction(fn func(tx *gorm.DB) error) error {
	if err := operationGate.enter(); err != nil {
		return err
	}
	defer operationGate.leave()
	return DB.Transaction(fn)
}

const maintenanceGateToken = "cineinsight:database-maintenance-gate"

func registerMaintenanceCallbacks(db *gorm.DB) error {
	before := func(tx *gorm.DB) {
		if hasMaintenanceAccess(tx) {
			return
		}
		if err := operationGate.enter(); err != nil {
			tx.AddError(err)
			return
		}
		tx.InstanceSet(maintenanceGateToken, true)
	}
	after := func(tx *gorm.DB) {
		if acquired, ok := tx.InstanceGet(maintenanceGateToken); ok && acquired == true {
			tx.InstanceSet(maintenanceGateToken, false)
			operationGate.leave()
		}
	}

	registrations := []error{
		db.Callback().Create().Before("gorm:begin_transaction").Register("cineinsight:maintenance_before_create", before),
		db.Callback().Create().After("gorm:commit_or_rollback_transaction").Register("cineinsight:maintenance_after_create", after),
		db.Callback().Update().Before("gorm:begin_transaction").Register("cineinsight:maintenance_before_update", before),
		db.Callback().Update().After("gorm:commit_or_rollback_transaction").Register("cineinsight:maintenance_after_update", after),
		db.Callback().Delete().Before("gorm:begin_transaction").Register("cineinsight:maintenance_before_delete", before),
		db.Callback().Delete().After("gorm:commit_or_rollback_transaction").Register("cineinsight:maintenance_after_delete", after),
		db.Callback().Query().Before("gorm:query").Register("cineinsight:maintenance_before_query", before),
		db.Callback().Query().After("gorm:after_query").Register("cineinsight:maintenance_after_query", after),
		db.Callback().Raw().Before("gorm:raw").Register("cineinsight:maintenance_before_raw", before),
		db.Callback().Raw().After("gorm:raw").Register("cineinsight:maintenance_after_raw", after),
		db.Callback().Row().Before("gorm:row").Register("cineinsight:maintenance_before_row", before),
		db.Callback().Row().After("gorm:row").Register("cineinsight:maintenance_after_row", after),
	}
	return errors.Join(registrations...)
}
